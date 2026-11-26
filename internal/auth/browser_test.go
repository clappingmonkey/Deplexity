package auth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func TestIsPerplexityDomain(t *testing.T) {
	for domain, want := range map[string]bool{
		"perplexity.ai":          true,
		".perplexity.ai":         true,
		"www.perplexity.ai":      true,
		".WWW.Perplexity.AI":     true,
		"notperplexity.ai":       false,
		"perplexity.ai.evil.com": false,
		"..perplexity.ai":        true,
		"evil.com":               false,
		"":                       false,
	} {
		if got := isPerplexityDomain(domain); got != want {
			t.Errorf("isPerplexityDomain(%q) = %t, want %t", domain, got, want)
		}
	}
}

func TestFindSessionFromCookies(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * 24 * time.Hour)
	later := now.Add(60 * 24 * time.Hour)
	past := float64(now.Add(-time.Minute).Unix())
	sessionWith := func(value, domain string, expires float64) *network.Cookie {
		return &network.Cookie{Name: sessionCookieName, Value: value, Domain: domain, Expires: expires}
	}
	session := func(domain string, expires float64) *network.Cookie {
		return sessionWith("tok", domain, expires)
	}

	tests := []struct {
		name        string
		cookies     []*network.Cookie
		wantSession bool
		wantToken   string // defaults to "tok"
		wantCSRF    string
		wantExpires time.Time
		wantCookies []string // names in order
	}{
		{name: "no cookies"},
		{
			name:        "cookie expiry is used",
			cookies:     []*network.Cookie{session(".perplexity.ai", float64(future.Unix()))},
			wantSession: true,
			wantExpires: future,
			wantCookies: []string{sessionCookieName},
		},
		{
			name:        "session-only cookie falls back",
			cookies:     []*network.Cookie{session("www.perplexity.ai", -1)},
			wantSession: true,
			wantExpires: now.Add(sessionFallbackTTL),
			wantCookies: []string{sessionCookieName},
		},
		{
			name:    "expired cookie is ignored",
			cookies: []*network.Cookie{session(".perplexity.ai", past)},
		},
		{
			name:    "lookalike domain is ignored",
			cookies: []*network.Cookie{session("notperplexity.ai", float64(future.Unix()))},
		},
		{
			name: "foreign cookies are dropped, csrf kept",
			cookies: []*network.Cookie{
				nil,
				{Name: "other", Value: "x", Domain: "evil.com"},
				{Name: csrfCookieName, Value: "csrf", Domain: "www.perplexity.ai"},
				session(".perplexity.ai", float64(future.Unix())),
			},
			wantSession: true,
			wantCSRF:    "csrf",
			wantExpires: future,
			wantCookies: []string{sessionCookieName, csrfCookieName},
		},
		{
			name:        "live cookie wins over a later expired one",
			cookies:     []*network.Cookie{session(".perplexity.ai", float64(future.Unix())), sessionWith("old", "www.perplexity.ai", past)},
			wantSession: true,
			wantExpires: future,
			wantCookies: []string{sessionCookieName},
		},
		{
			name:        "live cookie wins over an earlier expired one",
			cookies:     []*network.Cookie{sessionWith("old", "www.perplexity.ai", past), session(".perplexity.ai", float64(future.Unix()))},
			wantSession: true,
			wantExpires: future,
			wantCookies: []string{sessionCookieName},
		},
		{
			name:        "latest expiry wins",
			cookies:     []*network.Cookie{sessionWith("old", ".perplexity.ai", float64(future.Unix())), sessionWith("new", "www.perplexity.ai", float64(later.Unix())), sessionWith("mid", "perplexity.ai", float64(future.Unix()))},
			wantSession: true,
			wantToken:   "new",
			wantExpires: later,
			wantCookies: []string{sessionCookieName},
		},
		{
			name:        "dated cookie wins over session-only",
			cookies:     []*network.Cookie{sessionWith("transient", ".perplexity.ai", -1), session("www.perplexity.ai", float64(future.Unix())), sessionWith("transient2", ".perplexity.ai", 0)},
			wantSession: true,
			wantExpires: future,
			wantCookies: []string{sessionCookieName},
		},
		{
			name:        "empty value is ignored",
			cookies:     []*network.Cookie{session(".perplexity.ai", -1), sessionWith("", "www.perplexity.ai", float64(later.Unix()))},
			wantSession: true,
			wantExpires: now.Add(sessionFallbackTTL),
			wantCookies: []string{sessionCookieName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findSessionFromCookies(tt.cookies, now)
			if (got != nil) != tt.wantSession {
				t.Fatalf("session = %+v, want session %t", got, tt.wantSession)
			}
			if got == nil {
				return
			}
			wantToken := tt.wantToken
			if wantToken == "" {
				wantToken = "tok"
			}
			if got.SessionToken != wantToken {
				t.Errorf("SessionToken = %q, want %q", got.SessionToken, wantToken)
			}
			if got.Cookies[0].Value != wantToken {
				t.Errorf("first cookie value = %q, want the chosen token %q", got.Cookies[0].Value, wantToken)
			}
			if got.CSRFToken != tt.wantCSRF {
				t.Errorf("CSRFToken = %q, want %q", got.CSRFToken, tt.wantCSRF)
			}
			if !got.ExpiresAt.Equal(tt.wantExpires) {
				t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, tt.wantExpires)
			}
			var names []string
			for _, c := range got.Cookies {
				names = append(names, c.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.wantCookies, ",") {
				t.Errorf("cookies = %v, want %v", names, tt.wantCookies)
			}
		})
	}
}

// TestChosenSessionCookieIsTheOnlyOneSaved checks the persisted cookies, which
// the API client sends verbatim (see client.TestCookieHeaderSendsSavedCookies).
func TestChosenSessionCookieIsTheOnlyOneSaved(t *testing.T) {
	now := time.Now()
	session := findSessionFromCookies([]*network.Cookie{
		{Name: sessionCookieName, Value: "expired", Domain: ".perplexity.ai", Expires: float64(now.Add(-time.Hour).Unix())},
		{Name: sessionCookieName, Value: "older", Domain: "www.perplexity.ai", Expires: float64(now.Add(time.Hour).Unix())},
		{Name: sessionCookieName, Value: "chosen", Domain: "perplexity.ai", Expires: float64(now.Add(48 * time.Hour).Unix())},
		{Name: csrfCookieName, Value: "csrf", Domain: "www.perplexity.ai"},
	}, now)
	if session == nil {
		t.Fatal("no session found")
	}
	var values []string
	for _, c := range session.Cookies {
		if c.Name == sessionCookieName {
			values = append(values, c.Value)
		}
	}
	if len(values) != 1 || values[0] != "chosen" {
		t.Fatalf("saved session cookies = %v, want only [chosen]", values)
	}
}

func TestCookieExpiryKeepsFraction(t *testing.T) {
	if got, want := cookieExpiry(1.5), time.UnixMilli(1500).UTC(); !got.Equal(want) {
		t.Errorf("cookieExpiry(1.5) = %v, want %v", got, want)
	}
	for _, s := range []float64{0, -1} {
		if got := cookieExpiry(s); !got.IsZero() {
			t.Errorf("cookieExpiry(%v) = %v, want zero", s, got)
		}
	}
}

func TestBrowserLoginCanceledContextDoesNotLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	session, err := BrowserLogin(ctx, "/nonexistent/browser")
	if session != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("BrowserLogin = (%v, %v), want context.Canceled", session, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("BrowserLogin took %s on a canceled context", elapsed)
	}
}

func TestBrowserLoginMissingBrowser(t *testing.T) {
	_, err := BrowserLogin(context.Background(), "/nonexistent/browser")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("BrowserLogin error = %v, want browser not found", err)
	}
}

func TestLoginError(t *testing.T) {
	base := errors.New("boom")

	canceledParent, cancel := context.WithCancel(context.Background())
	cancel()
	if err := loginError(canceledParent, canceledParent, time.Minute, base); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled parent: %v, want context.Canceled", err)
	}

	timedOut, cancelTimeout := context.WithTimeout(context.Background(), 0)
	defer cancelTimeout()
	<-timedOut.Done()
	if err := loginError(context.Background(), timedOut, time.Minute, base); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %v, want context.DeadlineExceeded", err)
	}

	if err := loginError(context.Background(), context.Background(), time.Minute, context.Canceled); !errors.Is(err, errLoginWindowClosed) {
		t.Errorf("lost browser: %v, want errLoginWindowClosed", err)
	}

	if err := loginError(context.Background(), context.Background(), time.Minute, base); !errors.Is(err, base) {
		t.Errorf("other error: %v, want %v", err, base)
	}

	parentDeadline, cancelParent := context.WithTimeout(context.Background(), 0)
	defer cancelParent()
	<-parentDeadline.Done()
	err := loginError(parentDeadline, parentDeadline, time.Minute, base)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "canceled") {
		t.Errorf("parent deadline: %v, want a deadline error not worded as a cancel", err)
	}
}

func TestStartError(t *testing.T) {
	bg := context.Background()
	output := &tailBuffer{max: 16}
	_, _ = output.Write([]byte("0123456789abcdefTAIL"))

	if err := startError(bg, bg, time.Minute, chromedp.ErrNoDisplay, output); !errors.Is(err, errNeedsDisplay) || !strings.Contains(err.Error(), "--cookie") {
		t.Errorf("no display: %v, want errNeedsDisplay with a --cookie hint", err)
	}

	// An early exit shows the bounded tail, not chromedp's full message.
	err := startError(bg, bg, time.Minute, errors.New("chrome failed to start:\n"+strings.Repeat("x", 5000)), output)
	if !errors.Is(err, errBrowserExited) || !strings.HasSuffix(err.Error(), "456789abcdefTAIL") || strings.Contains(err.Error(), "xxx") {
		t.Errorf("early exit: %q, want errBrowserExited with the output tail", err)
	}

	// With no output, the start error itself is the explanation.
	err = startError(bg, bg, time.Minute, os.ErrPermission, &tailBuffer{max: 16})
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("silent start failure: %v, want the original error kept", err)
	}

	canceled, cancel := context.WithCancel(bg)
	cancel()
	if err := startError(canceled, canceled, time.Minute, context.Canceled, output); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v, want context.Canceled", err)
	}
}

func TestBrowserErrorTellsCrashFromClose(t *testing.T) {
	bg := context.Background()
	output := &tailBuffer{max: 64}
	_, _ = output.Write([]byte("Received signal 11"))

	crash := exec.Command(os.Args[0], "-test.run=^$", "-test.bogus-flag")
	exitErr := crash.Run()
	var ee *exec.ExitError
	if !errors.As(exitErr, &ee) {
		t.Fatalf("setup: %v is not an *exec.ExitError", exitErr)
	}
	crashed := errors.Join(exitErr, context.Canceled)
	if err := browserError(bg, bg, time.Minute, crashed, output); !errors.Is(err, errBrowserExited) || !strings.Contains(err.Error(), "signal 11") {
		t.Errorf("crash: %v, want errBrowserExited with output", err)
	}

	closed := errors.Join(errors.New("exit status 0"), context.Canceled)
	if err := browserError(bg, bg, time.Minute, closed, output); !errors.Is(err, errLoginWindowClosed) {
		t.Errorf("closed: %v, want errLoginWindowClosed", err)
	}

	canceled, cancel := context.WithCancel(bg)
	cancel()
	if err := browserError(canceled, canceled, time.Minute, crashed, output); !errors.Is(err, context.Canceled) || errors.Is(err, errBrowserExited) {
		t.Errorf("canceled: %v, want the cancel to win over the exit", err)
	}
}

func TestLoginAllocatorOptionsDoNotMutateDefaults(t *testing.T) {
	before := len(chromedp.DefaultExecAllocatorOptions)
	cfg := loginConfig{execPath: "/bin/chrome", visible: true, flags: map[string]string{"x": ""}}
	_ = loginAllocatorOptions(cfg)
	_ = loginAllocatorOptions(cfg)
	if len(chromedp.DefaultExecAllocatorOptions) != before {
		t.Fatal("loginAllocatorOptions changed chromedp.DefaultExecAllocatorOptions")
	}
}

func TestWaitForSessionRejectsZeroInterval(t *testing.T) {
	_, err := waitForSession(context.Background(),
		func() ([]*network.Cookie, error) { return nil, nil }, windowAlwaysOpen, 0)
	if err == nil {
		t.Fatal("waitForSession accepted a zero interval")
	}
}

func TestHasPageTarget(t *testing.T) {
	if hasPageTarget([]*target.Info{nil, {Type: "browser"}, {Type: "service_worker"}}) {
		t.Error("hasPageTarget without pages = true")
	}
	if !hasPageTarget([]*target.Info{{Type: "browser"}, {Type: "page"}}) {
		t.Error("hasPageTarget with a page = false")
	}
}

func TestFindBrowser(t *testing.T) {
	present := map[string]bool{}
	exists := func(p string) bool { return present[p] }
	onPath := map[string]string{}
	lookPath := func(name string) (string, error) {
		if p, ok := onPath[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	reset := func() {
		clear(present)
		clear(onPath)
	}

	t.Run("explicit wins over env", func(t *testing.T) {
		reset()
		present["/opt/a"], present["/opt/b"] = true, true
		if got, err := findBrowser("/opt/a", "/opt/b", "linux", exists, lookPath); err != nil || got != "/opt/a" {
			t.Fatalf("findBrowser = (%q, %v), want /opt/a", got, err)
		}
	})
	t.Run("env name resolved on PATH", func(t *testing.T) {
		reset()
		onPath["brave"] = "/usr/bin/brave"
		if got, err := findBrowser("", "brave", "linux", exists, lookPath); err != nil || got != "/usr/bin/brave" {
			t.Fatalf("findBrowser = (%q, %v), want /usr/bin/brave", got, err)
		}
	})
	t.Run("missing override does not fall back", func(t *testing.T) {
		reset()
		onPath["google-chrome"] = "/usr/bin/google-chrome"
		_, err := findBrowser("/missing", "", "linux", exists, lookPath)
		if err == nil || !strings.Contains(err.Error(), "--cookie") {
			t.Fatalf("findBrowser = %v, want a not-found error with a --cookie hint", err)
		}
	})
	t.Run("bare name ignores the current directory", func(t *testing.T) {
		reset()
		present["chrome"] = true // a file named chrome in the working directory
		if got, err := findBrowser("chrome", "", "linux", exists, lookPath); err == nil {
			t.Fatalf("findBrowser = %q, want the bare name looked up on PATH only", got)
		}
		onPath["chrome"] = "/usr/bin/chrome"
		if got, err := findBrowser("chrome", "", "linux", exists, lookPath); err != nil || got != "/usr/bin/chrome" {
			t.Fatalf("findBrowser = (%q, %v), want /usr/bin/chrome", got, err)
		}
	})
	t.Run("windows PATH names", func(t *testing.T) {
		reset()
		onPath["msedge"] = `C:\Edge\msedge.exe`
		if got, err := findBrowser("", "", "windows", exists, lookPath); err != nil || got != `C:\Edge\msedge.exe` {
			t.Fatalf("findBrowser = (%q, %v), want msedge from PATH", got, err)
		}
	})
	t.Run("macOS application bundle", func(t *testing.T) {
		reset()
		present["/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"] = true
		got, err := findBrowser("", "", "darwin", exists, lookPath)
		if err != nil || !strings.HasSuffix(got, "Brave Browser") {
			t.Fatalf("findBrowser = (%q, %v), want Brave", got, err)
		}
	})
	t.Run("linux PATH order", func(t *testing.T) {
		reset()
		onPath["chromium"] = "/usr/bin/chromium"
		onPath["microsoft-edge"] = "/usr/bin/microsoft-edge"
		if got, err := findBrowser("", "", "linux", exists, lookPath); err != nil || got != "/usr/bin/chromium" {
			t.Fatalf("findBrowser = (%q, %v), want /usr/bin/chromium", got, err)
		}
	})
	t.Run("none found suggests --cookie", func(t *testing.T) {
		reset()
		_, err := findBrowser("", "", "linux", exists, lookPath)
		if !errors.Is(err, errNoBrowser) || !strings.Contains(err.Error(), "--cookie") {
			t.Fatalf("findBrowser error = %v, want errNoBrowser with a --cookie hint", err)
		}
	})
}

func liveSessionCookies() []*network.Cookie {
	return []*network.Cookie{{Name: sessionCookieName, Value: "tok", Domain: ".perplexity.ai", Expires: -1}}
}

func windowAlwaysOpen() (bool, error) { return true, nil }

func TestWaitForSessionStopsWhenWindowCloses(t *testing.T) {
	calls, checks := 0, 0
	_, err := waitForSession(context.Background(), func() ([]*network.Cookie, error) {
		calls++
		return nil, nil
	}, func() (bool, error) {
		checks++
		switch checks {
		case 1:
			return false, errors.New("transient") // errors do not end the wait
		case 2:
			return true, nil
		default:
			return false, nil
		}
	}, time.Millisecond)
	if !errors.Is(err, errLoginWindowClosed) {
		t.Fatalf("waitForSession error = %v, want errLoginWindowClosed", err)
	}
	if calls != 3 {
		t.Errorf("cookie reads = %d, want 3", calls)
	}
}

func TestWaitForSession(t *testing.T) {
	readErr := errors.New("connection closed")
	tests := []struct {
		name      string
		responses []error // nil = cookies returned (session on the last call)
		wantErr   error
		wantCalls int
	}{
		{name: "found after polling", responses: []error{nil, nil, nil}, wantCalls: 3},
		{name: "browser gone", responses: []error{readErr, readErr, readErr, nil}, wantErr: readErr, wantCalls: 3},
		{name: "failures reset on success", responses: []error{readErr, readErr, nil, readErr, readErr, nil}, wantCalls: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			session, err := waitForSession(context.Background(), func() ([]*network.Cookie, error) {
				i := calls
				calls++
				if tt.responses[i] != nil {
					return nil, tt.responses[i]
				}
				if i == len(tt.responses)-1 {
					return liveSessionCookies(), nil
				}
				return nil, nil
			}, windowAlwaysOpen, time.Millisecond)
			if calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tt.wantCalls)
			}
			if tt.wantErr != nil {
				if session != nil || !errors.Is(err, tt.wantErr) {
					t.Fatalf("waitForSession = (%v, %v), want %v", session, err, tt.wantErr)
				}
				return
			}
			if err != nil || session == nil {
				t.Fatalf("waitForSession = (%v, %v), want a session", session, err)
			}
		})
	}
}

func TestWaitForSessionStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := waitForSession(ctx, func() ([]*network.Cookie, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil, nil
	}, windowAlwaysOpen, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForSession error = %v, want context.Canceled", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestWaitForSessionReadErrorAfterCancelReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForSession(ctx, func() ([]*network.Cookie, error) {
		return nil, errors.New("pipe closed")
	}, windowAlwaysOpen, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForSession error = %v, want context.Canceled", err)
	}
}

func TestTailBufferKeepsLastBytes(t *testing.T) {
	b := &tailBuffer{max: 5}
	for _, s := range []string{"abc", "defg", "h"} {
		if n, err := b.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := b.String(); got != "defgh" {
		t.Errorf("String() = %q, want %q", got, "defgh")
	}
}

func TestBrowserExitedError(t *testing.T) {
	if err := browserExitedError("  \n"); err != errBrowserExited {
		t.Errorf("empty output: got %v, want errBrowserExited", err)
	}
	err := browserExitedError("crash\xff here\n")
	if !errors.Is(err, errBrowserExited) {
		t.Fatalf("got %v, want it to wrap errBrowserExited", err)
	}
	if !strings.HasSuffix(err.Error(), "browser output:\ncrash? here") {
		t.Errorf("error = %q, want sanitized output appended", err)
	}
}
