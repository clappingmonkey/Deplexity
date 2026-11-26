package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	helperModeEnv    = "DEPLEXITY_AUTH_TEST_HELPER"
	helperPIDFileEnv = "DEPLEXITY_AUTH_TEST_PIDFILE"

	helperModeReal         = "real"
	helperModeSession      = "session"
	helperModeWindowClosed = "window_closed"
	helperModeCrash        = "crash"
	helperModeHang         = "hang"
	helperModeGarbage      = "garbage"
	helperModeSleep        = "sleep"
	helperModeLateSession  = "late_session"
	helperModeDetach       = "detach"
)

// TestMain lets the test binary act as the login helper, or as a stand-in
// browser process, when helperModeEnv is set.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(runTestHelper(mode))
	}
	os.Exit(m.Run())
}

func runTestHelper(mode string) int {
	switch mode {
	case helperModeReal:
		if err := RunBrowserLoginHelper(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case helperModeSleep:
		time.Sleep(time.Minute)
		return 0
	}

	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadBytes('\n')
	if err != nil {
		return 1
	}
	var req helperRequest
	if err := json.Unmarshal(line, &req); err != nil || req.validate() != nil {
		return 1
	}
	// Leave a file behind, so removal of a non-empty profile is tested.
	_ = os.WriteFile(filepath.Join(req.ProfileDir, "Cookies"), []byte("x"), 0o600)
	out := json.NewEncoder(os.Stdout)

	switch mode {
	case helperModeSession:
		_ = out.Encode(helperMessage{Done: true, Session: &models.SavedSession{SessionToken: "tok"}})
	case helperModeWindowClosed:
		_ = out.Encode(helperMessage{Done: true, Error: errLoginWindowClosed.Error(), Kind: "window_closed"})
	case helperModeLateSession:
		// The session arrives just as the parent stops the helper.
		_, _ = in.ReadBytes('\n')
		_ = out.Encode(helperMessage{Done: true, Session: &models.SavedSession{SessionToken: "late"}})
		return 0
	case helperModeGarbage:
		_, _ = os.Stdout.WriteString(strings.Repeat("x", maxHelperMessage+1))
		return 0
	case helperModeDetach:
		// A browser child that left the helper's process group and
		// outlives the helper, still naming the profile.
		child := exec.Command(os.Args[0], "--user-data-dir="+req.ProfileDir)
		child.Env = append(os.Environ(), helperModeEnv+"="+helperModeSleep)
		detach(child)
		if err := child.Start(); err != nil {
			return 1
		}
		_ = os.WriteFile(os.Getenv(helperPIDFileEnv), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		_ = out.Encode(helperMessage{Done: true, Session: &models.SavedSession{SessionToken: "tok"}})
	case helperModeCrash, helperModeHang:
		browser := exec.Command(os.Args[0])
		browser.Env = append(os.Environ(), helperModeEnv+"="+helperModeSleep)
		if err := browser.Start(); err != nil {
			return 1
		}
		_ = os.WriteFile(os.Getenv(helperPIDFileEnv), []byte(strconv.Itoa(browser.Process.Pid)), 0o600)
		if mode == helperModeCrash {
			panic("boom in the cdp reader")
		}
		time.Sleep(time.Hour) // ignores stdin EOF, like a teardown that hangs
	}
	_, _ = in.ReadBytes('\n') // wait for stdin EOF
	return 0
}

// testHelperCommand runs this test binary as the helper in mode.
func testHelperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode)
	return cmd
}

// pidFile returns a file the crash and hang helpers write their fake
// browser's process ID to, and sets it in cmd's environment.
func pidFile(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "browser.pid")
	cmd.Env = append(cmd.Env, helperPIDFileEnv+"="+path)
	return path
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the fake browser pid: %v", err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// testRequest names the test binary as the browser, because the fake
// browsers the fake helpers start are copies of it.
func testRequest() helperRequest {
	exe, _ := os.Executable()
	return helperRequest{ExecPath: exe, URL: "https://example.test/", Timeout: time.Minute, Poll: time.Second}
}

// profileDirs lists the login profiles superviseLogin creates.
func profileDirs(t *testing.T) map[string]bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "deplexity-login-*"))
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, m := range matches {
		dirs[m] = true
	}
	return dirs
}

// isolatedProfiles points TMPDIR at a directory of this test's own, so
// profiles from other tests or a real login are not counted, and returns the
// profiles already there.
func isolatedProfiles(t *testing.T) map[string]bool {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	return profileDirs(t)
}

func assertNoNewProfiles(t *testing.T, before map[string]bool) {
	t.Helper()
	for dir := range profileDirs(t) {
		if !before[dir] {
			t.Errorf("login profile %s was left behind", dir)
		}
	}
}

func shortExitTimeout(t *testing.T) {
	t.Helper()
	old := helperExitTimeout
	helperExitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { helperExitTimeout = old })
}

func TestSuperviseLoginReturnsSession(t *testing.T) {
	before := isolatedProfiles(t)
	session, err := superviseLogin(context.Background(), testRequest(), testHelperCommand(t, helperModeSession))
	if err != nil {
		t.Fatalf("superviseLogin: %v", err)
	}
	if session.SessionToken != "tok" {
		t.Errorf("SessionToken = %q, want tok", session.SessionToken)
	}
	assertNoNewProfiles(t, before)
}

func TestSuperviseLoginKeepsHelperErrorKind(t *testing.T) {
	_, err := superviseLogin(context.Background(), testRequest(), testHelperCommand(t, helperModeWindowClosed))
	if !errors.Is(err, errLoginWindowClosed) {
		t.Fatalf("superviseLogin error = %v, want errLoginWindowClosed", err)
	}
}

func TestSuperviseLoginReportsHelperCrash(t *testing.T) {
	before := isolatedProfiles(t)
	cmd := testHelperCommand(t, helperModeCrash)
	pids := pidFile(t, cmd)

	_, err := superviseLogin(context.Background(), testRequest(), cmd)
	if !errors.Is(err, errLoginHelperFailed) || !strings.Contains(err.Error(), "boom in the cdp reader") {
		t.Fatalf("superviseLogin error = %v, want errLoginHelperFailed with the panic", err)
	}
	assertProcessGone(t, readPID(t, pids))
	assertNoNewProfiles(t, before)
}

func TestSuperviseLoginRejectsOversizedOutput(t *testing.T) {
	_, err := superviseLogin(context.Background(), testRequest(), testHelperCommand(t, helperModeGarbage))
	if !errors.Is(err, errLoginHelperFailed) {
		t.Fatalf("superviseLogin error = %v, want errLoginHelperFailed", err)
	}
}

func TestSuperviseLoginCancelKillsHungHelper(t *testing.T) {
	shortExitTimeout(t)
	before := isolatedProfiles(t)
	cmd := testHelperCommand(t, helperModeHang)
	pids := pidFile(t, cmd)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// A canceled parent, not a deadline: superviseLogin must say "canceled".
	ctx, cancelNow := context.WithCancel(ctx)
	time.AfterFunc(500*time.Millisecond, cancelNow)

	start := time.Now()
	_, err := superviseLogin(ctx, testRequest(), cmd)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("superviseLogin error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("superviseLogin took %s after cancel", elapsed)
	}
	assertProcessGone(t, readPID(t, pids))
	assertNoNewProfiles(t, before)
}

// A session that arrives while the helper is being stopped must not be lost
// to the cancel or timeout that started the stop.
func TestSuperviseLoginKeepsLateSession(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(300*time.Millisecond, cancel)
		session, err := superviseLogin(ctx, testRequest(), testHelperCommand(t, helperModeLateSession))
		if err != nil || session.SessionToken != "late" {
			t.Fatalf("superviseLogin = (%v, %v), want the late session", session, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		req := testRequest()
		req.Timeout = 300 * time.Millisecond
		session, err := superviseLogin(context.Background(), req, testHelperCommand(t, helperModeLateSession))
		if err != nil || session.SessionToken != "late" {
			t.Fatalf("superviseLogin = (%v, %v), want the late session", session, err)
		}
	})
}

// A browser child that escaped the helper's process group must not outlive
// the login and write into the removed profile.
func TestSuperviseLoginKillsDetachedBrowserChild(t *testing.T) {
	before := isolatedProfiles(t)
	cmd := testHelperCommand(t, helperModeDetach)
	pids := pidFile(t, cmd)

	session, err := superviseLogin(context.Background(), testRequest(), cmd)
	if err != nil || session.SessionToken != "tok" {
		t.Fatalf("superviseLogin = (%v, %v), want the session", session, err)
	}
	assertProcessGone(t, readPID(t, pids))
	assertNoNewProfiles(t, before)
}

func TestSuperviseLoginTimesOut(t *testing.T) {
	shortExitTimeout(t)
	req := testRequest()
	req.Timeout = 300 * time.Millisecond
	cmd := testHelperCommand(t, helperModeHang)
	pids := pidFile(t, cmd)

	_, err := superviseLogin(context.Background(), req, cmd)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("superviseLogin error = %v, want a login timeout", err)
	}
	assertProcessGone(t, readPID(t, pids))
}

func TestKillLoginHelpers(t *testing.T) {
	cmd := testHelperCommand(t, helperModeHang)
	pids := pidFile(t, cmd)
	errCh := make(chan error, 1)
	go func() {
		_, err := superviseLogin(context.Background(), testRequest(), cmd)
		errCh <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(pids); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never started its fake browser")
		}
		time.Sleep(20 * time.Millisecond)
	}
	KillLoginHelpers()

	select {
	case err := <-errCh:
		if !errors.Is(err, errLoginHelperFailed) {
			t.Errorf("superviseLogin error = %v, want errLoginHelperFailed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superviseLogin did not return after KillLoginHelpers")
	}
	assertProcessGone(t, readPID(t, pids))
}

// registerTestLogin puts a login that has not started into the force-quit
// registry, as superviseLogin does right after creating the profile.
func registerTestLogin(t *testing.T) *loginHelper {
	t.Helper()
	profile := filepath.Join(t.TempDir(), loginProfilePrefix+"test")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	h := &loginHelper{cmd: testHelperCommand(t, helperModeSession), profile: profile, browser: testRequest().ExecPath}
	running.mu.Lock()
	running.helpers[h] = struct{}{}
	running.mu.Unlock()
	t.Cleanup(func() {
		running.mu.Lock()
		delete(running.helpers, h)
		running.mu.Unlock()
	})
	return h
}

func TestKillLoginHelpersStopsLoginBeforeStart(t *testing.T) {
	h := registerTestLogin(t)
	if err := KillLoginHelpers(); err != nil {
		t.Fatalf("KillLoginHelpers: %v", err)
	}
	if _, err := os.Stat(h.profile); !os.IsNotExist(err) {
		t.Errorf("profile still exists: %v", err)
	}
	if err := startHelper(h); !errors.Is(err, errLoginStopped) {
		t.Fatalf("startHelper after force quit = %v, want errLoginStopped", err)
	}
	if h.cmd.Process != nil {
		t.Error("the helper was started after the force quit")
	}
}

func TestKillLoginHelpersIsBoundedWhileAStartIsStuck(t *testing.T) {
	h := registerTestLogin(t)
	h.mu.Lock() // a start that does not return
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			h.mu.Unlock()
		}
	}
	t.Cleanup(unlock)

	start := time.Now()
	err := killLoginHelpers(200 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("killLoginHelpers took %s with a stuck start", elapsed)
	}
	if !errors.Is(err, errProfileNotRemoved) {
		t.Errorf("killLoginHelpers = %v, want errProfileNotRemoved", err)
	}
	unlock()
}

func TestHelperRequestValidate(t *testing.T) {
	ok := testRequest()
	ok.ProfileDir = filepath.Join(os.TempDir(), loginProfilePrefix+"123")
	if err := ok.validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	home, _ := os.UserHomeDir()
	for name, mutate := range map[string]func(*helperRequest){
		"browser":             func(r *helperRequest) { r.ExecPath = "" },
		"profile":             func(r *helperRequest) { r.ProfileDir = "" },
		"profile outside tmp": func(r *helperRequest) { r.ProfileDir = filepath.Join(home, loginProfilePrefix+"x") },
		"profile wrong name":  func(r *helperRequest) { r.ProfileDir = filepath.Join(os.TempDir(), "important") },
		"profile is tmp":      func(r *helperRequest) { r.ProfileDir = os.TempDir() },
		"profile nested":      func(r *helperRequest) { r.ProfileDir = filepath.Join(os.TempDir(), "a", loginProfilePrefix+"x") },
		"profile traversal":   func(r *helperRequest) { r.ProfileDir = filepath.Join(os.TempDir(), loginProfilePrefix+"x", "..", "..") },
		"timeout":             func(r *helperRequest) { r.Timeout = 0 },
		"poll":                func(r *helperRequest) { r.Poll = -1 },
	} {
		r := ok
		mutate(&r)
		if r.validate() == nil {
			t.Errorf("request without %s passed validation", name)
		}
	}
}

func TestRunBrowserLoginHelperRejectsBadRequest(t *testing.T) {
	var out strings.Builder
	if err := RunBrowserLoginHelper(strings.NewReader("{}\n"), &out); err == nil {
		t.Fatal("RunBrowserLoginHelper accepted an empty request")
	}
	if err := RunBrowserLoginHelper(strings.NewReader("not json\n"), &out); err == nil {
		t.Fatal("RunBrowserLoginHelper accepted a malformed request")
	}
	if out.Len() != 0 {
		t.Errorf("helper wrote %q for a bad request", out.String())
	}
}

func TestHelperErrorKindRoundTrip(t *testing.T) {
	for _, sentinel := range []error{errLoginWindowClosed, errBrowserExited, errNeedsDisplay, context.DeadlineExceeded, context.Canceled} {
		wrapped := fmt.Errorf("detail: %w", sentinel)
		msg := helperMessage{Error: wrapped.Error(), Kind: errorKind(wrapped)}
		got := msg.err()
		if !errors.Is(got, sentinel) || got.Error() != wrapped.Error() {
			t.Errorf("round trip of %v = %v", sentinel, got)
		}
	}
	plain := helperMessage{Error: "other"}
	if got := plain.err(); got.Error() != "other" {
		t.Errorf("plain error = %v", got)
	}
}

func TestHelperWatchdog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	aborted := make(chan struct{})
	go helperWatchdog(ctx, finished, 50*time.Millisecond, func() { close(aborted) })
	cancel()
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not abort a login that ignored cancellation")
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	finished2 := make(chan struct{})
	done := make(chan struct{})
	go func() {
		helperWatchdog(ctx2, finished2, 50*time.Millisecond, func() { t.Error("watchdog aborted a finished login") })
		close(done)
	}()
	cancel2()
	close(finished2)
	<-done
	_ = finished
}
