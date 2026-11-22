package client

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// newTLSTestServer starts a TLS server that counts accepted connections.
func newTLSTestServer(t *testing.T, enableHTTP2 bool, handler http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = enableHTTP2
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &conns
}

// trustingTransport returns a chromeTransport that trusts srv's certificate.
func trustingTransport(srv *httptest.Server) *chromeTransport {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return newChromeTransportWithConfig(&utls.Config{RootCAs: pool})
}

func TestChromeTransportReusesConnections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		http2     bool
		wantProto int
	}{
		{name: "HTTP2", http2: true, wantProto: 2},
		{name: "HTTP1", http2: false, wantProto: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, conns := newTLSTestServer(t, tc.http2, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Flush headers first and stream the body slowly, the case that
				// previously leaked one connection per request.
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
				_, _ = io.WriteString(w, "ok")
			}))
			tr := trustingTransport(srv)
			defer tr.CloseIdleConnections()
			hc := &http.Client{Transport: tr}

			const requests = 5
			for i := range requests {
				resp, err := hc.Get(srv.URL)
				if err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != "ok" {
					t.Fatalf("request %d body = %q", i, body)
				}
				if resp.ProtoMajor != tc.wantProto {
					t.Fatalf("request %d proto = %s, want HTTP/%d", i, resp.Proto, tc.wantProto)
				}
			}
			if got := conns.Load(); got != 1 {
				t.Errorf("server accepted %d connections for %d requests, want 1", got, requests)
			}
		})
	}
}

func TestChromeTransportRejectsNonHTTPS(t *testing.T) {
	tr := newChromeTransport()
	req, _ := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, errInsecureScheme) {
		t.Fatalf("RoundTrip error = %v, want errInsecureScheme", err)
	}
}

func TestClientDoesNotRetryUntrustedCertificate(t *testing.T) {
	var calls atomic.Int32
	srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "{}")
	}))

	waits := 0
	c := &Client{
		// System roots only: the httptest certificate is untrusted.
		http:    &http.Client{Transport: newChromeTransport()},
		baseURL: srv.URL,
		retryWait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	}

	err := c.Get(context.Background(), "/test", nil)
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) {
		t.Fatalf("Get error = %v, want x509.UnknownAuthorityError", err)
	}
	if waits != 0 || calls.Load() != 0 {
		t.Errorf("waits=%d handler calls=%d, want no retries and no handled request", waits, calls.Load())
	}
}

func TestClientRejectsRedirectToHTTP(t *testing.T) {
	var calls atomic.Int32
	srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "http://127.0.0.1:1/plain", http.StatusFound)
	}))

	waits := 0
	c := &Client{
		http:    &http.Client{Transport: trustingTransport(srv), CheckRedirect: checkRedirect},
		baseURL: srv.URL,
		retryWait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	}

	err := c.Get(context.Background(), "/test", nil)
	if !errors.Is(err, errRedirectRejected) {
		t.Fatalf("Get error = %v, want errRedirectRejected", err)
	}
	if waits != 0 || calls.Load() != 1 {
		t.Errorf("waits=%d calls=%d, want a single attempt", waits, calls.Load())
	}
}

func TestCheckRedirectBoundsChain(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/next", nil)
	if err := checkRedirect(req, make([]*http.Request, maxRedirects-1)); err != nil {
		t.Fatalf("checkRedirect below limit = %v, want nil", err)
	}
	if err := checkRedirect(req, make([]*http.Request, maxRedirects)); !errors.Is(err, errRedirectRejected) {
		t.Fatalf("checkRedirect at limit = %v, want errRedirectRejected", err)
	}
}

func TestCheckRedirectRedactsQuery(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://user:pass@example.test/p?X-Amz-Signature=secret", nil)
	err := checkRedirect(req, nil)
	if err == nil {
		t.Fatal("checkRedirect accepted a non-https redirect")
	}
	if msg := err.Error(); strings.Contains(msg, "secret") || strings.Contains(msg, "pass") {
		t.Errorf("redirect error leaks credentials: %q", msg)
	}
}

func TestChromeTransportHandshakeTimeout(t *testing.T) {
	old := tlsHandshakeTimeout
	tlsHandshakeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { tlsHandshakeTimeout = old })

	// Accept TCP but never speak TLS.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read returns once the client gives up and closes the socket.
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()

	tr := newChromeTransport()
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+"/", nil)

	start := time.Now()
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip succeeded against a silent server")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("RoundTrip took %s, want the handshake timeout to fire", elapsed)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("stalled handshake connection was never closed")
	}
}

func TestClientRedactsSignedQueryInTransportErrors(t *testing.T) {
	const marker = "sig-marker"

	t.Run("certificate", func(t *testing.T) {
		srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		c := &Client{http: &http.Client{Transport: newChromeTransport()}, baseURL: "https://www.perplexity.ai"}
		_, err := c.GetRawURL(context.Background(), srv.URL+"/skill.md?X-Amz-Signature="+marker)
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("error = %v, want redacted certificate failure", err)
		}
		var unknownAuthority x509.UnknownAuthorityError
		if !errors.As(err, &unknownAuthority) {
			t.Errorf("redaction lost the cause: %v", err)
		}
	})

	t.Run("network", func(t *testing.T) {
		wantErr := errors.New("connection reset")
		var logs strings.Builder
		log.SetOutput(&logs)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		c := &Client{
			http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, wantErr
			})},
			baseURL:   "https://www.perplexity.ai",
			verbose:   true,
			retryWait: func(context.Context, time.Duration) error { return nil },
		}
		_, err := c.GetRawURL(context.Background(), "https://bucket.example.test/skill.md?X-Amz-Signature="+marker)
		if err == nil || strings.Contains(err.Error(), marker) || !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want redacted wrapped network failure", err)
		}
		if strings.Contains(logs.String(), marker) {
			t.Errorf("verbose log leaks signed query: %q", logs.String())
		}
	})

	t.Run("redirect", func(t *testing.T) {
		srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://bucket.example.test/x?X-Amz-Signature="+marker, http.StatusFound)
		}))
		c := &Client{http: &http.Client{Transport: trustingTransport(srv), CheckRedirect: checkRedirect}, baseURL: "https://www.perplexity.ai"}
		_, err := c.GetRawURL(context.Background(), srv.URL+"/skill.md")
		if !errors.Is(err, errRedirectRejected) || strings.Contains(err.Error(), marker) {
			t.Fatalf("error = %v, want redacted redirect rejection", err)
		}
	})
}

func TestPermanentTransportErrorClassification(t *testing.T) {
	for name, err := range map[string]error{
		"utls verification wrapping system roots": &url.Error{Op: "Get", URL: "https://x", Err: &utls.CertificateVerificationError{Err: x509.SystemRootsError{}}},
		"unknown authority":                       &url.Error{Op: "Get", URL: "https://x", Err: x509.UnknownAuthorityError{}},
		"redirect":                                &url.Error{Op: "Get", URL: "https://x", Err: errRedirectRejected},
	} {
		if !isPermanentTransportError(err) {
			t.Errorf("%s: classified as retryable", name)
		}
	}
	if isPermanentTransportError(&url.Error{Op: "Get", URL: "https://x", Err: errors.New("connection reset")}) {
		t.Error("plain network error classified as permanent")
	}
}

func TestClientReusesConnectionAfterErrorResponse(t *testing.T) {
	srv, conns := newTLSTestServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied")
	}))
	tr := trustingTransport(srv)
	defer tr.CloseIdleConnections()
	c := &Client{http: &http.Client{Transport: tr}, baseURL: srv.URL}

	for range 3 {
		if err := c.Get(context.Background(), "/test", nil); !errors.Is(err, ErrNotAuthenticated) {
			t.Fatalf("Get error = %v, want ErrNotAuthenticated", err)
		}
		if _, err := c.GetRawURL(context.Background(), srv.URL+"/raw"); err == nil {
			t.Fatal("GetRawURL accepted HTTP 403")
		}
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("server accepted %d connections, want 1 (error bodies must be drained)", got)
	}
}

func TestRedirectRefererDropsSignedQuery(t *testing.T) {
	const marker = "sig-marker"
	var referer atomic.Value
	srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		referer.Store(r.Header.Get("Referer"))
		_, _ = io.WriteString(w, "body")
	}))
	c := &Client{http: &http.Client{Transport: trustingTransport(srv), CheckRedirect: checkRedirect}, baseURL: "https://www.perplexity.ai"}

	body, err := c.GetRawURL(context.Background(), srv.URL+"/start?X-Amz-Signature="+marker)
	if err != nil {
		t.Fatalf("GetRawURL: %v", err)
	}
	if string(body) != "body" {
		t.Fatalf("body = %q, want redirect to be followed", body)
	}
	got, _ := referer.Load().(string)
	if strings.Contains(got, marker) {
		t.Errorf("redirected request Referer = %q, leaks signed query", got)
	}
}

func TestClientRedactsMalformedRedirectLocation(t *testing.T) {
	const marker = "sig-marker"
	srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://bucket.example.test/%zz?X-Amz-Signature="+marker)
		w.WriteHeader(http.StatusFound)
	}))
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	c := &Client{
		http:      &http.Client{Transport: trustingTransport(srv), CheckRedirect: checkRedirect},
		baseURL:   "https://www.perplexity.ai",
		verbose:   true,
		retryWait: func(context.Context, time.Duration) error { return nil },
	}

	_, err := c.GetRawURL(context.Background(), srv.URL+"/skill.md?X-Amz-Signature="+marker)
	if err == nil {
		t.Fatal("GetRawURL followed a malformed Location")
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("error leaks signed query: %q", err)
	}
	if strings.Contains(logs.String(), marker) {
		t.Errorf("verbose log leaks signed query: %q", logs.String())
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Errorf("redaction lost the *url.Error cause: %v", err)
	}
}

func TestRedactURLErrorNeverShowsRawText(t *testing.T) {
	const secret = "secret-marker"
	for name, raw := range map[string]string{
		"query":         "https://h/p?X-Amz-Signature=" + secret,
		"single quote":  "https://h/p?note='&X-Amz-Signature=" + secret,
		"escaped quote": `https://h/p?note=\"&X-Amz-Signature=` + secret,
		"tab":           "https://h/%zz\t?X-Amz-Signature=" + secret,
		"userinfo":      "http://user:" + secret + "@h/x",
		"fragment":      "https://h/x#" + secret,
	} {
		t.Run(name, func(t *testing.T) {
			inner := fmt.Errorf("failed to parse Location header %q: invalid", raw)
			err := redactURLError(&url.Error{Op: "Get", URL: raw, Err: inner})
			if strings.Contains(err.Error(), secret) {
				t.Errorf("message = %q, leaks secret", err)
			}
			if !errors.Is(err, inner) {
				t.Error("redacted error does not unwrap to the cause")
			}
		})
	}
}

func TestDescribeTransportErrorKeepsSafeDetail(t *testing.T) {
	for want, err := range map[string]error{
		"request timed out":                      context.DeadlineExceeded,
		"TLS certificate verification failed":    &utls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		`DNS lookup failed for host "h.example"`: &net.DNSError{Name: "h.example", Err: "no such host"},
		"dial tcp: connection refused":           &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		"transport error":                        errors.New("https://h/?sig=secret"),
	} {
		if got := describeTransportError(err); got != want {
			t.Errorf("describeTransportError(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestClientRedactsRejectedRedirectUserinfoAndFragment(t *testing.T) {
	srv, _ := newTLSTestServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://user:password-marker@h.example/x#fragment-marker")
		w.WriteHeader(http.StatusFound)
	}))
	c := &Client{http: &http.Client{Transport: trustingTransport(srv), CheckRedirect: checkRedirect}, baseURL: "https://www.perplexity.ai"}

	_, err := c.GetRawURL(context.Background(), srv.URL+"/skill.md")
	if !errors.Is(err, errRedirectRejected) {
		t.Fatalf("error = %v, want errRedirectRejected", err)
	}
	if msg := err.Error(); strings.Contains(msg, "password-marker") || strings.Contains(msg, "fragment-marker") {
		t.Errorf("error leaks userinfo or fragment: %q", msg)
	}
}
