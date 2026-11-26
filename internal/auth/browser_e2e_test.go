package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// browserTestEnv names a Chromium-based browser executable. The end-to-end
// tests skip when it is unset, so `go test ./...` needs no browser.
const browserTestEnv = "DEPLEXITY_TEST_BROWSER"

// fakePerplexity serves https://www.perplexity.ai/ from a local TLS server and
// returns the browser flags that route the real hostname to it and trust its
// certificate.
func fakePerplexity(t *testing.T, handler http.HandlerFunc) map[string]string {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	spki := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return map[string]string{
		"host-resolver-rules":                 "MAP www.perplexity.ai 127.0.0.1:" + port,
		"ignore-certificate-errors-spki-list": base64.StdEncoding.EncodeToString(spki[:]),
	}
}

// e2eConfig is headless unless DEPLEXITY_TEST_BROWSER_VISIBLE is set, so CI
// runners need no display.
func e2eConfig(t *testing.T, flags map[string]string) loginConfig {
	t.Helper()
	path := os.Getenv(browserTestEnv)
	if path == "" {
		t.Skipf("set %s to a Chrome/Chromium executable to run browser login tests", browserTestEnv)
	}
	return loginConfig{
		execPath: path,
		url:      perplexityURL,
		timeout:  time.Minute,
		poll:     50 * time.Millisecond,
		visible:  os.Getenv("DEPLEXITY_TEST_BROWSER_VISIBLE") != "",
		flags:    flags,
	}
}

func TestBrowserLoginE2EThroughHelper(t *testing.T) {
	cfg := e2eConfig(t, fakePerplexity(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "helper-token", Domain: ".perplexity.ai", Path: "/", Secure: true})
		_, _ = w.Write([]byte("<!doctype html><title>Perplexity</title>"))
	}))
	before := isolatedProfiles(t)

	session, err := superviseLogin(context.Background(), helperRequest{
		ExecPath: cfg.execPath, URL: cfg.url, Timeout: cfg.timeout, Poll: cfg.poll, Visible: cfg.visible, Flags: cfg.flags,
	}, testHelperCommand(t, helperModeReal))
	if err != nil {
		t.Fatalf("superviseLogin: %v", err)
	}
	if session.SessionToken != "helper-token" {
		t.Errorf("SessionToken = %q, want helper-token", session.SessionToken)
	}
	assertNoNewProfiles(t, before)
}

// The helper removes the profile itself, so a parent that dies right after
// reading the result still leaves nothing behind.
func TestBrowserLoginE2EHelperRemovesProfileWithoutParent(t *testing.T) {
	cfg := e2eConfig(t, fakePerplexity(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "orphan-token", Domain: ".perplexity.ai", Path: "/", Secure: true})
		_, _ = w.Write([]byte("<!doctype html><title>Perplexity</title>"))
	}))
	t.Cleanup(func() { signal.Reset(os.Interrupt, syscall.SIGPIPE) })

	profile, err := os.MkdirTemp("", loginProfilePrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(profile) })
	req, err := json.Marshal(helperRequest{
		ExecPath: cfg.execPath, URL: cfg.url, ProfileDir: profile,
		Timeout: cfg.timeout, Poll: cfg.poll, Visible: cfg.visible, Flags: cfg.flags,
	})
	if err != nil {
		t.Fatal(err)
	}

	// stdin stays open: the parent never asks the helper to stop.
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	go func() { _, _ = stdinW.Write(append(req, '\n')) }()

	// The "parent" reads the result and then disappears.
	stdoutR, stdoutW := io.Pipe()
	got := make(chan helperMessage, 1)
	go func() {
		var m helperMessage
		_ = json.NewDecoder(stdoutR).Decode(&m)
		_ = stdoutR.Close()
		got <- m
	}()

	// The parent kills the helper as soon as its output ends, so the profile
	// must already be gone by then.
	probe := &closeProbe{WriteCloser: stdoutW, onClose: func() error {
		_, err := os.Stat(profile)
		return err
	}}
	if err := RunBrowserLoginHelper(stdinR, probe); err != nil {
		t.Fatalf("RunBrowserLoginHelper: %v", err)
	}
	if m := <-got; m.Session == nil || m.Session.SessionToken != "orphan-token" {
		t.Fatalf("result = %+v, want the session", m)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("profile %s still exists after the helper returned (stat err %v)", profile, err)
	}
	if !probe.closed || !errors.Is(probe.err, os.ErrNotExist) {
		t.Errorf("stdout closed=%v with profile stat err %v, want closed after the profile was removed", probe.closed, probe.err)
	}
}

// runnerDirs lists chromedp's temporary profile directories.
func runnerDirs(t *testing.T) map[string]bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "chromedp-runner*"))
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, m := range matches {
		dirs[m] = true
	}
	return dirs
}

func assertProfileRemoved(t *testing.T, before map[string]bool) {
	t.Helper()
	for dir := range runnerDirs(t) {
		if !before[dir] {
			t.Errorf("browser profile %s was left behind", dir)
		}
	}
}

func TestBrowserLoginE2ECapturesSessionCookie(t *testing.T) {
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	cfg := e2eConfig(t, fakePerplexity(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "e2e-token", Domain: ".perplexity.ai", Path: "/", Expires: expires, Secure: true, HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "e2e-csrf", Path: "/", Secure: true})
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>Perplexity</title>"))
	}))
	before := runnerDirs(t)

	session, err := runBrowserLogin(context.Background(), cfg)
	if err != nil {
		t.Fatalf("runBrowserLogin: %v", err)
	}
	if session.SessionToken != "e2e-token" || session.CSRFToken != "e2e-csrf" {
		t.Errorf("session tokens = (%q, %q), want (e2e-token, e2e-csrf)", session.SessionToken, session.CSRFToken)
	}
	// The browser keeps sub-second precision from when it parsed the header.
	if d := session.ExpiresAt.Sub(expires); d < 0 || d >= time.Second {
		t.Errorf("ExpiresAt = %v, want the cookie expiry %v", session.ExpiresAt, expires)
	}
	assertProfileRemoved(t, before)
}

func TestBrowserLoginE2EWindowClosed(t *testing.T) {
	cfg := e2eConfig(t, fakePerplexity(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>login</title>"))
	}))
	cfg.started = func(tabCtx context.Context) {
		targets, err := chromedp.Targets(tabCtx)
		if err != nil {
			t.Errorf("Targets: %v", err)
			return
		}
		for _, info := range targets {
			if info.Type == "page" {
				_, _ = chromedp.CallBrowser(tabCtx, target.CloseTarget, target.CloseTargetParams{TargetID: info.TargetID})
			}
		}
	}
	before := runnerDirs(t)

	start := time.Now()
	_, err := runBrowserLogin(context.Background(), cfg)
	if !errors.Is(err, errLoginWindowClosed) {
		t.Fatalf("runBrowserLogin error = %v, want errLoginWindowClosed", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("closing the window took %s to notice", elapsed)
	}
	assertProfileRemoved(t, before)
}

func TestBrowserLoginE2ECancel(t *testing.T) {
	cfg := e2eConfig(t, fakePerplexity(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>login</title>"))
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cfg.started = func(context.Context) { cancel() }
	before := runnerDirs(t)

	start := time.Now()
	_, err := runBrowserLogin(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runBrowserLogin error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("cancel took %s", elapsed)
	}
	assertProfileRemoved(t, before)
}

// closeProbe records the result of onClose at the moment Close is called.
type closeProbe struct {
	io.WriteCloser
	onClose func() error
	closed  bool
	err     error
}

func (p *closeProbe) Close() error {
	p.closed, p.err = true, p.onClose()
	return p.WriteCloser.Close()
}
