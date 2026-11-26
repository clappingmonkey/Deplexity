package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// BrowserHelperArg is the hidden argument that makes the deplexity binary run
// RunBrowserLoginHelper instead of the CLI.
const BrowserHelperArg = "__browser-login-helper"

const (
	maxHelperRequest = 64 << 10
	maxHelperMessage = 1 << 20
	// profileRemoveTimeout bounds retries for a profile the browser still
	// holds open (Windows keeps files locked until the process is gone).
	profileRemoveTimeout = 5 * time.Second
	// leftoverSweepTimeout bounds the search for browser processes that
	// outlived the browser (see sweepProfileProcesses).
	leftoverSweepTimeout = 2 * time.Second
	// profileCleanupTimeout bounds the whole of cleanProfile.
	profileCleanupTimeout = leftoverSweepTimeout + profileRemoveTimeout
	// forceCleanupTimeout bounds the whole of KillLoginHelpers, however many
	// helpers run.
	forceCleanupTimeout = 2 * time.Second
)

// helperExitTimeout is how long the parent waits for the helper to close the
// browser, clean up the profile and exit before it kills both. It is a
// variable for tests.
var helperExitTimeout = browserCloseTimeout + profileCleanupTimeout + time.Second

// errProfileNotRemoved means the throwaway browser profile, which holds the
// session cookie, is still on disk.
var errProfileNotRemoved = errors.New("the temporary browser profile could not be removed")

// errLoginStopped is returned when a force quit stopped a login before its
// helper started.
var errLoginStopped = errors.New("login was stopped")

var errLoginHelperFailed = errors.New("browser login helper exited unexpectedly")

// helperRequest is the one message the parent sends to the helper.
type helperRequest struct {
	ExecPath   string            `json:"exec_path"`
	URL        string            `json:"url"`
	ProfileDir string            `json:"profile_dir"`
	Timeout    time.Duration     `json:"timeout"`
	Poll       time.Duration     `json:"poll"`
	Visible    bool              `json:"visible"`
	Flags      map[string]string `json:"flags,omitempty"`
}

// loginProfilePrefix names the throwaway profiles superviseLogin creates.
const loginProfilePrefix = "deplexity-login-"

// isLoginProfileDir reports whether dir is a direct child of the temp
// directory, in either its given or its resolved spelling, named like a login
// profile.
func isLoginProfileDir(dir string) bool {
	dir = filepath.Clean(dir)
	if !strings.HasPrefix(filepath.Base(dir), loginProfilePrefix) {
		return false
	}
	parent, tmp := filepath.Dir(dir), filepath.Clean(os.TempDir())
	if parent == tmp {
		return true
	}
	resolved, err := filepath.EvalSymlinks(tmp)
	return err == nil && parent == resolved
}

// canonicalPath resolves the symlinks in dir's parent, so the path passed to
// the browser is the one the sweep looks for (macOS /var is /private/var).
// It works after dir itself was removed.
func canonicalPath(dir string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		return filepath.Clean(dir)
	}
	return filepath.Join(parent, filepath.Base(dir))
}

func (r helperRequest) validate() error {
	switch {
	case r.ExecPath == "", r.URL == "", r.ProfileDir == "":
		return errors.New("helper request is missing a browser, URL or profile")
	case !isLoginProfileDir(r.ProfileDir):
		// The helper deletes the profile when it exits, so it must never
		// accept a directory superviseLogin did not create.
		return fmt.Errorf("helper profile %q is not a %s* directory in %s", r.ProfileDir, loginProfilePrefix, os.TempDir())
	case r.Timeout <= 0, r.Poll <= 0:
		return errors.New("helper request has no timeout or poll interval")
	}
	return nil
}

// helperMessage is the line the helper writes to stdout with its result.
type helperMessage struct {
	Done    bool                 `json:"done,omitempty"`
	Session *models.SavedSession `json:"session,omitempty"`
	Error   string               `json:"error,omitempty"`
	Kind    string               `json:"kind,omitempty"`
}

// helperErrorKinds carries error identity across the process boundary.
var helperErrorKinds = []struct {
	name string
	err  error
}{
	{"window_closed", errLoginWindowClosed},
	{"browser_exited", errBrowserExited},
	{"no_display", errNeedsDisplay},
	{"timeout", context.DeadlineExceeded},
	{"canceled", context.Canceled},
}

func errorKind(err error) string {
	for _, k := range helperErrorKinds {
		if errors.Is(err, k.err) {
			return k.name
		}
	}
	return ""
}

// helperError is an error reported by the helper. It keeps the helper's
// message and unwraps to the sentinel its kind names.
type helperError struct {
	msg  string
	kind error
}

func (e *helperError) Error() string { return e.msg }
func (e *helperError) Unwrap() error { return e.kind }

func (m *helperMessage) err() error {
	for _, k := range helperErrorKinds {
		if m.Kind == k.name {
			return &helperError{msg: m.Error, kind: k.err}
		}
	}
	return errors.New(m.Error)
}

// loginHelper is a login as seen by the parent: its profile, and once
// started, the helper process. It never signals a bare process ID: the
// helper's process group (unix) or job (Windows) is only killed before the
// helper is reaped, so it cannot have been reused.
type loginHelper struct {
	cmd     *exec.Cmd
	profile string
	browser string

	// mu is held while the helper starts, so kill either prevents the start
	// or sees the started tree.
	mu      sync.Mutex
	tree    *procTree
	reaped  bool
	stopped bool
}

// kill kills the helper and everything it started, unless it was reaped,
// and makes a helper that has not started yet never start.
func (h *loginHelper) kill() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	if h.tree != nil && !h.reaped {
		h.tree.kill()
	}
}

// reap marks the helper as reaped, so kill no longer touches its group,
// then waits for it and releases its job.
func (h *loginHelper) reap() {
	h.mu.Lock()
	h.reaped = true
	h.mu.Unlock()
	_ = h.cmd.Wait()
	h.tree.release()
}

// running tracks the logins KillLoginHelpers must clean up. A login is in it
// from the moment its profile exists until the profile was removed.
var running = struct {
	mu      sync.Mutex
	helpers map[*loginHelper]struct{}
}{helpers: map[*loginHelper]struct{}{}}

// KillLoginHelpers kills every running login helper and its browser, and
// removes their profiles, returning within forceCleanupTimeout in total. The
// CLI calls it before force-quitting, because os.Exit skips the cleanup
// BrowserLogin would otherwise do. The error names the profiles that are
// still on disk. A helper it could not reach in time still has its stdin
// closed by the exit, and then cleans up after itself.
func KillLoginHelpers() error {
	return killLoginHelpers(forceCleanupTimeout)
}

func killLoginHelpers(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	done := make(chan error, 1)
	go func() {
		running.mu.Lock()
		helpers := make([]*loginHelper, 0, len(running.helpers))
		for h := range running.helpers {
			helpers = append(helpers, h)
		}
		running.mu.Unlock()

		errs := make([]error, len(helpers))
		var wg sync.WaitGroup
		for i, h := range helpers {
			wg.Go(func() {
				h.kill()
				errs[i] = cleanProfile(h.profile, h.browser, time.Until(deadline))
			})
		}
		wg.Wait()
		done <- errors.Join(errs...)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%w: cleanup did not finish within %s", errProfileNotRemoved, timeout)
	}
}

// startHelper starts h.cmd and takes ownership of its process tree, unless
// a force quit already stopped h. A helper that cannot be owned is killed,
// because it could not be cleaned up later. The helper starts no browser
// before it reads its request, which is sent after this returns, so on
// Windows the browser is always created inside the job.
func startHelper(h *loginHelper) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return errLoginStopped
	}
	if err := h.cmd.Start(); err != nil {
		return err
	}
	tree, err := newProcTree(h.cmd.Process)
	if err != nil {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
		return err
	}
	h.tree = tree
	return nil
}

func newHelperCommand() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the deplexity executable: %w", err)
	}
	return exec.Command(exe, BrowserHelperArg), nil
}

// superviseLogin runs the login in the helper started by cmd. It creates the
// profile, enforces req.Timeout and ctx, and on every path closes the
// helper's stdin, gives it helperExitTimeout to shut down, kills what is
// left, and removes the profile. When the profile cannot be removed the
// error wraps errProfileNotRemoved, and a session that was captured is still
// returned.
func superviseLogin(ctx context.Context, req helperRequest, cmd *exec.Cmd) (session *models.SavedSession, err error) {
	profile, err := os.MkdirTemp("", loginProfilePrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("creating the browser profile: %w", err)
	}
	profile = canonicalPath(profile)
	req.ProfileDir = profile

	// Registered until the profile is gone, so a force quit during any part
	// of the login, including this cleanup, still removes it.
	h := &loginHelper{cmd: cmd, profile: profile, browser: req.ExecPath}
	running.mu.Lock()
	running.helpers[h] = struct{}{}
	running.mu.Unlock()
	defer func() {
		if cerr := cleanProfile(profile, req.ExecPath, profileCleanupTimeout); cerr != nil {
			err = errors.Join(err, cerr)
		}
		running.mu.Lock()
		delete(running.helpers, h)
		running.mu.Unlock()
	}()

	loginCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	stderr := &tailBuffer{max: browserOutputTail}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	prepareTree(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("starting the browser login helper: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("starting the browser login helper: %w", err)
	}

	if err = startHelper(h); err != nil {
		return nil, fmt.Errorf("starting the browser login helper: %w", err)
	}

	msgs := make(chan helperMessage)
	go readHelperMessages(stdout, msgs)

	result := awaitHelper(loginCtx, stdin, req, msgs)
	result = stopHelper(h, stdin, msgs, result)

	// A session that arrived wins over a cancel or timeout that came while
	// the helper was shutting down: the user has already signed in.
	switch {
	case result != nil && result.Error == "" && result.Session != nil:
		return result.Session, nil
	case ctx.Err() != nil || loginCtx.Err() != nil:
		return nil, loginError(ctx, loginCtx, req.Timeout, context.Canceled)
	case result == nil:
		return nil, helperFailedError(stderr.String())
	case result.Error != "":
		return nil, result.err()
	}
	return nil, helperFailedError(stderr.String())
}

// awaitHelper sends req and waits for the result. It returns nil when ctx
// ends or the helper exits without a result.
func awaitHelper(ctx context.Context, stdin io.Writer, req helperRequest, msgs <-chan helperMessage) *helperMessage {
	if err := json.NewEncoder(stdin).Encode(req); err != nil {
		return nil
	}
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return nil
			}
			if m.Done {
				return &m
			}
		case <-ctx.Done():
			// A result may be waiting already; stopHelper also keeps one
			// that arrives while the helper shuts down.
			return nil
		}
	}
}

// stopHelper closes stdin, which asks the helper to stop, waits up to
// helperExitTimeout for its output to end, kills what is left, and reaps it.
// It returns result, or the result the helper sent while stopping.
func stopHelper(h *loginHelper, stdin io.Closer, msgs <-chan helperMessage, result *helperMessage) *helperMessage {
	_ = stdin.Close()
	timer := time.NewTimer(helperExitTimeout)
	defer timer.Stop()

wait:
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				break wait
			}
			if m.Done && result == nil {
				result = &m
			}
		case <-timer.C:
			break wait
		}
	}
	// Kill before reaping, so the process group cannot have been reused yet.
	// After a clean exit this only reaches a browser the helper failed to
	// stop, or interrupts its own removal of the profile, which the parent
	// repeats anyway.
	h.kill()
	h.reap()
	for range msgs {
	}
	return result
}

// readHelperMessages decodes stdout lines into out and closes it at EOF. A
// line that is too long ends the decoding; the rest is discarded so the
// helper never blocks on a full pipe.
func readHelperMessages(r io.Reader, out chan<- helperMessage) {
	defer close(out)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxHelperMessage)
	for sc.Scan() {
		var m helperMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		out <- m
	}
	_, _ = io.Copy(io.Discard, r)
}

func helperFailedError(stderr string) error {
	stderr = strings.TrimSpace(strings.ToValidUTF8(stderr, "?"))
	if stderr == "" {
		return errLoginHelperFailed
	}
	return fmt.Errorf("%w; helper output:\n%s", errLoginHelperFailed, stderr)
}

// cleanProfile kills browser processes that still use the profile dir, then
// removes it, retrying while a browser that is shutting down still holds
// files in it. It returns within timeout even if a step blocks, and reports
// errProfileNotRemoved when dir may still exist.
func cleanProfile(dir, browser string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	done := make(chan error, 1)
	go func() { done <- removeProfileDir(dir, browser, deadline) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%w: %s: timed out after %s", errProfileNotRemoved, dir, timeout)
	}
}

func removeProfileDir(dir, browser string, deadline time.Time) error {
	sweepProfileProcesses(dir, browser, min(leftoverSweepTimeout, time.Until(deadline)))
	for {
		err := os.RemoveAll(dir)
		if err == nil {
			return nil
		}
		if time.Until(deadline) < 100*time.Millisecond {
			return fmt.Errorf("%w: %w", errProfileNotRemoved, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// RunBrowserLoginHelper is the helper process started by BrowserLogin. It
// reads one request from stdin, runs the login, and writes helperMessages to
// stdout. Closing stdin, or the parent dying, stops the login; if the browser
// has not shut down browserCloseTimeout later, the helper kills it, removes
// the profile, and exits.
func RunBrowserLoginHelper(stdin io.Reader, stdout io.Writer) error {
	// The parent owns Ctrl+C; on Windows the console delivers it here too.
	// SIGPIPE is ignored so a write to a dead parent returns an error
	// instead of killing the helper before it cleans up.
	signal.Ignore(os.Interrupt, syscall.SIGPIPE)

	in := bufio.NewReaderSize(stdin, maxHelperRequest)
	line, err := in.ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("reading the helper request: %w", err)
	}
	var req helperRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return fmt.Errorf("decoding the helper request: %w", err)
	}
	if err := req.validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, in)
		cancel()
	}()

	out := &helperWriter{enc: json.NewEncoder(stdout)}
	var browser atomic.Pointer[os.Process]
	finished := make(chan struct{})
	go helperWatchdog(ctx, finished, browserCloseTimeout, func() {
		// The handle chromedp waits on knows when the browser was reaped,
		// so this cannot hit a process that reused its ID.
		if p := browser.Load(); p != nil {
			_ = p.Kill()
		}
		_ = cleanProfile(req.ProfileDir, req.ExecPath, profileCleanupTimeout)
		os.Exit(2)
	})

	session, err := runBrowserLogin(ctx, loginConfig{
		execPath:   req.ExecPath,
		url:        req.URL,
		profileDir: req.ProfileDir,
		timeout:    req.Timeout,
		poll:       req.Poll,
		visible:    req.Visible,
		flags:      req.Flags,
		onBrowser:  browser.Store,
	})
	close(finished)

	msg := helperMessage{Done: true, Session: session}
	if err != nil {
		msg = helperMessage{Done: true, Error: err.Error(), Kind: errorKind(err)}
	}
	_ = out.send(msg)
	// Clean up before closing stdout: the parent kills the helper once its
	// output ends, and must not cut the cleanup short. The parent cleans up
	// too, so whichever process survives does it.
	if err := cleanProfile(req.ProfileDir, req.ExecPath, profileCleanupTimeout); err != nil {
		// The parent removes it too, and reports it if that fails as well.
		fmt.Fprintln(os.Stderr, err)
	}
	if c, ok := stdout.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

// helperWatchdog calls abort when the login has not finished timeout after
// ctx ended.
func helperWatchdog(ctx context.Context, finished <-chan struct{}, timeout time.Duration, abort func()) {
	select {
	case <-finished:
		return
	case <-ctx.Done():
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		if !isDone(finished) {
			abort()
		}
	}
}

func isDone(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// helperWriter writes helperMessages as JSON lines. A write error means the
// parent is gone; stdin EOF then stops the helper.
type helperWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *helperWriter) send(m helperMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(m)
}
