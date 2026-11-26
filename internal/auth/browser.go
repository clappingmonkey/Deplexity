package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	perplexityURL     = "https://www.perplexity.ai/"
	perplexityDomain  = "perplexity.ai"
	sessionCookieName = "__Secure-next-auth.session-token"
	csrfCookieName    = "next-auth.csrf-token"
	pollInterval      = 2 * time.Second
	loginTimeout      = 5 * time.Minute
	// maxCookieReadFailures ends the wait once the browser stops answering.
	maxCookieReadFailures = 3
	// browserCloseTimeout is how long the helper waits for the browser to
	// close before it kills it. The parent's helperExitTimeout is the hard
	// bound on the whole shutdown.
	browserCloseTimeout = 5 * time.Second
	// sessionFallbackTTL is used when the session cookie carries no expiry.
	sessionFallbackTTL = 7 * 24 * time.Hour
	// browserOutputTail is how much browser or helper output an error shows.
	browserOutputTail = 2048

	// BrowserEnv overrides browser discovery for BrowserLogin.
	BrowserEnv = "DEPLEXITY_BROWSER"
)

var (
	errLoginWindowClosed = errors.New("login window was closed before authentication completed")
	errNoBrowser         = errors.New("no Chrome, Chromium, Edge or Brave browser found")
	errBrowserExited     = errors.New("browser exited unexpectedly")
	errNeedsDisplay      = errors.New("browser login needs a display; use `deplexity login --cookie <TOKEN>` on headless machines")
)

// BrowserLogin opens a visible Chromium-based browser for the user to log in
// to Perplexity, waits for the session cookie, and returns the saved session.
// browserPath selects the browser executable; when empty, $DEPLEXITY_BROWSER
// and then well-known install locations are used.
//
// The browser runs under a helper process (see RunBrowserLoginHelper), so a
// hang or crash in the browser automation cannot hang or crash the CLI. This
// process owns the throwaway profile and the time limit, kills the helper and
// its browser when they do not stop in time, and always removes the profile.
// It gives up after loginTimeout, when ctx is canceled, or when the user
// closes the window.
func BrowserLogin(ctx context.Context, browserPath string) (*models.SavedSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("login canceled: %w", err)
	}
	execPath, err := findBrowser(browserPath, os.Getenv(BrowserEnv), runtime.GOOS, browserFileExists, exec.LookPath)
	if err != nil {
		return nil, err
	}
	cmd, err := newHelperCommand()
	if err != nil {
		return nil, err
	}

	fmt.Printf("Opening %s for login...\n", filepath.Base(execPath))
	fmt.Println("Please log in to Perplexity in the browser window.")
	fmt.Printf("Waiting for authentication (timeout: %s)...\n", loginTimeout)

	session, err := superviseLogin(ctx, helperRequest{
		ExecPath: execPath,
		URL:      perplexityURL,
		Timeout:  loginTimeout,
		Poll:     pollInterval,
		Visible:  true,
	}, cmd)
	if session != nil && errors.Is(err, errProfileNotRemoved) {
		// The login worked; only the cleanup did not. Keep the session, but
		// tell the user a copy of the cookie is still on disk.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		err = nil
	}
	if err != nil {
		return nil, err
	}
	fmt.Println("Authentication successful!")
	return session, nil
}

// loginConfig is what runBrowserLogin needs. Tests use it to point a real
// browser at a fake site and drive it.
type loginConfig struct {
	execPath string
	url      string
	// profileDir is the browser profile. When empty, chromedp creates and
	// removes its own.
	profileDir string
	timeout    time.Duration
	poll       time.Duration
	visible    bool
	// flags are extra browser flags; an empty value is a boolean flag.
	flags map[string]string
	// onBrowser, if set, receives the browser process once it started.
	onBrowser func(p *os.Process)
	// started, if set, runs after the first navigation with the tab context.
	started func(tabCtx context.Context)
}

// runBrowserLogin drives the browser. It runs in the helper process.
func runBrowserLogin(ctx context.Context, cfg loginConfig) (*models.SavedSession, error) {
	loginCtx, cancelLogin := context.WithTimeout(ctx, cfg.timeout)
	defer cancelLogin()

	output := &tailBuffer{max: browserOutputTail}
	opts := append(loginAllocatorOptions(cfg), chromedp.CombinedOutput(output))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(loginCtx, opts...)
	defer cancelAlloc()
	quiet := func(string, ...any) {}
	tabCtx, cancelTab := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(quiet), chromedp.WithErrorf(quiet), chromedp.WithDebugf(quiet))
	defer cancelTab()
	defer closeBrowser(tabCtx)

	// Start the browser first, so its process is known before anything can
	// hang.
	if err := chromedp.Do(tabCtx); err != nil {
		return nil, startError(ctx, loginCtx, cfg.timeout, err, output)
	}
	if cfg.onBrowser != nil {
		if p := chromedp.FromContext(tabCtx).Browser.Process(); p != nil {
			cfg.onBrowser(p)
		}
	}

	if err := chromedp.Do(tabCtx, chromedp.Navigate(cfg.url)); err != nil {
		return nil, browserError(ctx, loginCtx, cfg.timeout, err, output)
	}
	if cfg.started != nil {
		cfg.started(tabCtx)
	}

	session, err := waitForSession(tabCtx, func() ([]*network.Cookie, error) {
		res, err := chromedp.CallBrowser(tabCtx, storage.GetCookies, storage.GetCookiesParams{})
		if err != nil {
			return nil, err
		}
		return res.Cookies, nil
	}, func() (bool, error) {
		targets, err := chromedp.Targets(tabCtx)
		if err != nil {
			return false, err
		}
		return hasPageTarget(targets), nil
	}, cfg.poll)
	if err != nil {
		return nil, browserError(ctx, loginCtx, cfg.timeout, err, output)
	}
	return session, nil
}

// loginAllocatorOptions returns chromedp's defaults plus cfg. The defaults
// are headless; VisibleWindow also drops --enable-automation, so the login
// page sees an ordinary browser. Headless is not re-added after
// VisibleWindow, because chromedp would still require a display on Linux.
func loginAllocatorOptions(cfg loginConfig) []chromedp.ExecAllocatorOption {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(cfg.execPath))
	if cfg.profileDir != "" {
		opts = append(opts, chromedp.UserDataDir(cfg.profileDir))
	}
	for name, value := range cfg.flags {
		if value == "" {
			opts = append(opts, chromedp.Flag(name, true))
		} else {
			opts = append(opts, chromedp.Flag(name, value))
		}
	}
	// Last, so no extra flag can turn a visible window headless again.
	if cfg.visible {
		opts = append(opts, chromedp.VisibleWindow)
	}
	return opts
}

// closeBrowser asks the browser to close, waiting up to browserCloseTimeout
// for the request. chromedp then still waits for the process to exit; the
// helper's watchdog and the parent's kill bound that.
func closeBrowser(tabCtx context.Context) {
	closeCtx, cancel := context.WithTimeout(tabCtx, browserCloseTimeout)
	defer cancel()
	_ = chromedp.Cancel(closeCtx)
}

// startError explains a browser that could not be started.
func startError(parent, ctx context.Context, timeout time.Duration, err error, output *tailBuffer) error {
	switch {
	case errors.Is(err, chromedp.ErrNoDisplay):
		return errNeedsDisplay
	case parent.Err() != nil || ctx.Err() != nil:
		return loginError(parent, ctx, timeout, err)
	case strings.TrimSpace(output.String()) == "":
		// Nothing was printed, so the error itself (a bad executable, a
		// permission problem) is the only explanation.
		return fmt.Errorf("could not start browser: %w", err)
	default:
		// chromedp's own message repeats up to 64 KB of browser output;
		// show the bounded tail instead.
		return browserExitedError(output.String())
	}
}

// browserError explains why the login stopped after the browser started.
func browserError(parent, ctx context.Context, timeout time.Duration, err error, output *tailBuffer) error {
	if parent.Err() == nil && ctx.Err() == nil {
		// chromedp adds the process's exit error when the browser died on
		// its own. A user closing the last window exits with status 0,
		// which has no *exec.ExitError; a crash does not.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return browserExitedError(output.String())
		}
	}
	return loginError(parent, ctx, timeout, err)
}

// browserExitedError reports a browser that exited on its own, with the end
// of its output when there is any.
func browserExitedError(output string) error {
	output = strings.TrimSpace(strings.ToValidUTF8(output, "?"))
	if output == "" {
		return errBrowserExited
	}
	return fmt.Errorf("%w; browser output:\n%s", errBrowserExited, output)
}

// tailBuffer keeps the last max bytes written to it. It is safe for
// concurrent use.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if extra := len(b.buf) - b.max; extra > 0 {
		b.buf = append(b.buf[:0], b.buf[extra:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// loginError explains why the login stopped. parent is the caller's context;
// ctx is the login context, which also ends at the login timeout.
func loginError(parent, ctx context.Context, timeout time.Duration, err error) error {
	switch {
	case errors.Is(parent.Err(), context.DeadlineExceeded):
		return fmt.Errorf("login stopped: %w", parent.Err())
	case parent.Err() != nil:
		return fmt.Errorf("login canceled: %w", parent.Err())
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("login timed out after %s — please try again: %w", timeout, ctx.Err())
	case errors.Is(err, context.Canceled):
		// chromedp cancels its context when the browser connection is lost,
		// which is how closing the last window looks on Linux and Windows.
		return errLoginWindowClosed
	default:
		return err
	}
}

// waitForSession polls getCookies until a valid session cookie appears. It
// stops when ctx ends, when windowOpen reports that no page is left, or after
// maxCookieReadFailures consecutive cookie read errors.
func waitForSession(
	ctx context.Context,
	getCookies func() ([]*network.Cookie, error),
	windowOpen func() (bool, error),
	interval time.Duration,
) (*models.SavedSession, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("invalid poll interval %s", interval)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	for {
		cookies, err := getCookies()
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failures++
			if failures >= maxCookieReadFailures {
				return nil, fmt.Errorf("browser stopped responding: %w", err)
			}
		default:
			failures = 0
			if session := findSessionFromCookies(cookies, time.Now()); session != nil {
				return session, nil
			}
			// On macOS the browser keeps running after its last window closes.
			if open, err := windowOpen(); err == nil && !open {
				return nil, errLoginWindowClosed
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func hasPageTarget(targets []*target.Info) bool {
	for _, t := range targets {
		if t != nil && t.Type == "page" {
			return true
		}
	}
	return false
}

func isPerplexityDomain(domain string) bool {
	d := strings.ToLower(strings.TrimLeft(domain, "."))
	return d == perplexityDomain || strings.HasSuffix(d, "."+perplexityDomain)
}

// findSessionFromCookies builds a session from the browser's perplexity.ai
// cookies. Among several session cookies (e.g. on different subdomains), it
// picks a non-empty, unexpired one, preferring a dated cookie with the latest
// expiry over a session-only one. Only the chosen session cookie is kept, so
// the client never sends a stale or competing token.
func findSessionFromCookies(cookies []*network.Cookie, now time.Time) *models.SavedSession {
	var chosen *network.Cookie
	var chosenExpiry time.Time
	var others []models.Cookie
	csrf := ""

	for _, c := range cookies {
		if c == nil || !isPerplexityDomain(c.Domain) {
			continue
		}
		if c.Name != sessionCookieName {
			if c.Name == csrfCookieName {
				csrf = c.Value
			}
			others = append(others, toModelCookie(c))
			continue
		}
		if c.Value == "" {
			continue
		}
		expiry := cookieExpiry(c.Expires)
		if !expiry.IsZero() && !expiry.After(now) {
			continue
		}
		if chosen == nil || betterExpiry(expiry, chosenExpiry) {
			chosen, chosenExpiry = c, expiry
		}
	}
	if chosen == nil {
		return nil
	}

	expiresAt := chosenExpiry
	if expiresAt.IsZero() {
		expiresAt = now.Add(sessionFallbackTTL)
	}
	return &models.SavedSession{
		SessionToken: chosen.Value,
		CSRFToken:    csrf,
		Cookies:      append([]models.Cookie{toModelCookie(chosen)}, others...),
		ExpiresAt:    expiresAt,
	}
}

// betterExpiry reports whether candidate beats current; zero means session-only.
func betterExpiry(candidate, current time.Time) bool {
	if current.IsZero() {
		return !candidate.IsZero()
	}
	return candidate.After(current)
}

func toModelCookie(c *network.Cookie) models.Cookie {
	return models.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Domain:   c.Domain,
		Path:     c.Path,
		Expires:  c.Expires,
		HTTPOnly: c.HTTPOnly,
		Secure:   c.Secure,
	}
}

// cookieExpiry converts CDP's seconds-since-epoch (fractional, -1 or 0 for
// session cookies) into a UTC time, or the zero time for session cookies.
func cookieExpiry(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(seconds * 1000)).UTC()
}
