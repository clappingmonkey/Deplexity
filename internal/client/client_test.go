package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestClientGet(t *testing.T) {
	type response struct {
		Message string `json:"message"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test/endpoint" {
			http.NotFound(w, r)
			return
		}
		// Verify headers
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response{Message: "ok"})
	}))
	defer srv.Close()

	c := &Client{
		http:    srv.Client(),
		baseURL: srv.URL,
		delay:   0,
	}

	var resp response
	err := c.Get(context.Background(), "/test/endpoint", &resp)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Message != "ok" {
		t.Errorf("message = %q, want %q", resp.Message, "ok")
	}
}

func TestClientGetUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &Client{
		http:    srv.Client(),
		baseURL: srv.URL,
		delay:   0,
	}

	err := c.Get(context.Background(), "/anything", nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestClientGetContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{
		http:    srv.Client(),
		baseURL: srv.URL,
		delay:   0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := c.Get(ctx, "/test", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestRateLimitCancellationPreservesLastRequest(t *testing.T) {
	lastReq := time.Now()
	entered := make(chan struct{})
	c := &Client{
		delay:   time.Hour,
		lastReq: lastReq,
		waitDelay: func(ctx context.Context, _ time.Duration) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- c.rateLimit(ctx)
	}()

	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("rateLimit error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("rateLimit did not return promptly after cancellation")
	}
	if !c.lastReq.Equal(lastReq) {
		t.Errorf("lastReq = %v, want unchanged %v", c.lastReq, lastReq)
	}
}

func TestRateLimitWaitCompletionRecordsRequestTimestamp(t *testing.T) {
	previous := time.Now()
	waited := false
	c := &Client{
		delay:   time.Hour,
		lastReq: previous,
		waitDelay: func(context.Context, time.Duration) error {
			waited = true
			return nil
		},
	}
	if err := c.rateLimit(context.Background()); err != nil {
		t.Fatalf("rateLimit: %v", err)
	}
	if !waited {
		t.Fatal("rateLimit did not enter the wait path")
	}
	if !c.lastReq.After(previous) {
		t.Errorf("lastReq = %v, want after %v", c.lastReq, previous)
	}
}

func TestRateLimitRejectsPreCancelledContextWithoutTimestamp(t *testing.T) {
	c := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.rateLimit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("rateLimit error = %v, want context.Canceled", err)
	}
	if !c.lastReq.IsZero() {
		t.Errorf("lastReq = %v, want zero", c.lastReq)
	}
}

func TestRateLimitSuccessRecordsRequestTimestamp(t *testing.T) {
	c := &Client{}
	before := time.Now()
	if err := c.rateLimit(context.Background()); err != nil {
		t.Fatalf("rateLimit: %v", err)
	}
	if c.lastReq.Before(before) {
		t.Errorf("lastReq = %v, want at or after %v", c.lastReq, before)
	}
}

func TestClientMethodsCancellationDuringPacingSkipsRequest(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tests := []struct {
		name string
		call func(context.Context, *Client) error
	}{
		{name: "Get", call: func(ctx context.Context, c *Client) error { return c.Get(ctx, "/test", nil) }},
		{name: "GetRaw", call: func(ctx context.Context, c *Client) error { _, err := c.GetRaw(ctx, "/test"); return err }},
		{name: "GetRawURL", call: func(ctx context.Context, c *Client) error { _, err := c.GetRawURL(ctx, srv.URL+"/test"); return err }},
		{name: "Post", call: func(ctx context.Context, c *Client) error { return c.Post(ctx, "/test", nil, nil) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached atomic.Bool
			entered := make(chan struct{})
			clientHTTP := srv.Client()
			clientHTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reached.Store(true)
				return srv.Client().Transport.RoundTrip(req)
			})
			c := &Client{
				http:    clientHTTP,
				baseURL: srv.URL,
				delay:   time.Hour,
				lastReq: time.Now(),
				waitDelay: func(ctx context.Context, _ time.Duration) error {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- tt.call(ctx, c) }()
			<-entered
			cancel()

			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context.Canceled", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("request did not return promptly after pacing cancellation")
			}
			if reached.Load() {
				t.Fatal("HTTP transport was reached after pacing cancellation")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientMethodsDoNotWaitAfterFinalHTTPRetry(t *testing.T) {
	for _, method := range retryMethodCases() {
		t.Run(method.name, func(t *testing.T) {
			calls := 0
			waits := 0
			closed := 0
			c := &Client{
				http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Body:       &trackingReadCloser{Reader: strings.NewReader("unavailable"), closed: &closed},
						Header:     make(http.Header),
					}, nil
				})},
				baseURL: "https://example.test",
				retryWait: func(context.Context, time.Duration) error {
					waits++
					return nil
				},
			}

			err := method.call(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), "failed after 5 retries") {
				t.Fatalf("error = %v, want HTTP retry exhaustion", err)
			}
			if calls != maxRetries+1 || waits != maxRetries {
				t.Errorf("calls=%d waits=%d, want %d calls and %d waits", calls, waits, maxRetries+1, maxRetries)
			}
			if closed != calls {
				t.Errorf("closed bodies=%d, want %d", closed, calls)
			}
		})
	}
}

func TestClientMethodsSucceedOnFinalHTTPAttempt(t *testing.T) {
	for _, method := range retryMethodCases() {
		t.Run(method.name, func(t *testing.T) {
			calls := 0
			waits := 0
			c := &Client{
				http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					status := http.StatusServiceUnavailable
					if calls == maxRetries+1 {
						status = http.StatusOK
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
				})},
				baseURL: "https://example.test",
				retryWait: func(context.Context, time.Duration) error {
					waits++
					return nil
				},
			}

			if err := method.call(context.Background(), c); err != nil {
				t.Fatalf("final allowed attempt failed: %v", err)
			}
			if calls != maxRetries+1 || waits != maxRetries {
				t.Errorf("calls=%d waits=%d, want %d calls and %d waits", calls, waits, maxRetries+1, maxRetries)
			}
		})
	}
}

func TestClientMethodsDoNotWaitAfterFinalNetworkRetry(t *testing.T) {
	wantErr := errors.New("network unavailable")
	for _, method := range retryMethodCases() {
		t.Run(method.name, func(t *testing.T) {
			calls := 0
			waits := 0
			c := &Client{
				http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, wantErr
				})},
				baseURL: "https://example.test",
				retryWait: func(context.Context, time.Duration) error {
					waits++
					return nil
				},
			}

			err := method.call(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), "failed after 15 network retries") {
				t.Fatalf("error = %v, want network retry exhaustion", err)
			}
			if calls != maxNetworkRetries+1 || waits != maxNetworkRetries {
				t.Errorf("calls=%d waits=%d, want %d calls and %d waits", calls, waits, maxNetworkRetries+1, maxNetworkRetries)
			}
		})
	}
}

func TestClientMethodsSucceedOnFinalNetworkAttempt(t *testing.T) {
	wantErr := errors.New("network unavailable")
	for _, method := range retryMethodCases() {
		t.Run(method.name, func(t *testing.T) {
			calls := 0
			waits := 0
			c := &Client{
				http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					if calls <= maxNetworkRetries {
						return nil, wantErr
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
				})},
				baseURL: "https://example.test",
				retryWait: func(context.Context, time.Duration) error {
					waits++
					return nil
				},
			}

			if err := method.call(context.Background(), c); err != nil {
				t.Fatalf("final allowed attempt failed: %v", err)
			}
			if calls != maxNetworkRetries+1 || waits != maxNetworkRetries {
				t.Errorf("calls=%d waits=%d, want %d calls and %d waits", calls, waits, maxNetworkRetries+1, maxNetworkRetries)
			}
		})
	}
}

func TestRetryWaitCancellationStopsBeforeNextRequest(t *testing.T) {
	for _, method := range retryMethodCases() {
		t.Run(method.name, func(t *testing.T) {
			calls := 0
			entered := make(chan struct{})
			c := &Client{
				http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				})},
				baseURL: "https://example.test",
				retryWait: func(ctx context.Context, _ time.Duration) error {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- method.call(ctx, c) }()
			<-entered
			cancel()

			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context.Canceled", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("request did not return promptly after retry cancellation")
			}
			if calls != 1 {
				t.Errorf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestFinalRateLimitResponseUpdatesAdaptiveDelayWithoutWaiting(t *testing.T) {
	calls := 0
	waits := 0
	c := &Client{
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})},
		baseURL: "https://example.test",
		delay:   DefaultDelay / 4,
		waitDelay: func(context.Context, time.Duration) error {
			return nil
		},
		retryWait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	}

	err := c.Get(context.Background(), "/test", nil)
	if err == nil || !strings.Contains(err.Error(), "failed after 5 retries") {
		t.Fatalf("error = %v, want HTTP retry exhaustion", err)
	}
	if calls != maxRetries+1 || waits != maxRetries {
		t.Errorf("calls=%d waits=%d, want %d calls and %d waits", calls, waits, maxRetries+1, maxRetries)
	}
	if c.delay != maxDelay {
		t.Errorf("adaptive delay = %v, want capped %v", c.delay, maxDelay)
	}
}

func TestRawURLRateLimitDoesNotChangeAdaptiveDelay(t *testing.T) {
	calls := 0
	waits := 0
	c := &Client{
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})},
		baseURL: "https://example.test",
		delay:   DefaultDelay,
		waitDelay: func(context.Context, time.Duration) error {
			return nil
		},
		retryWait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	}

	_, err := c.GetRawURL(context.Background(), "https://example.test/test")
	if err == nil || !strings.Contains(err.Error(), "failed after 5 retries") {
		t.Fatalf("error = %v, want HTTP retry exhaustion", err)
	}
	if calls != maxRetries+1 || waits != maxRetries {
		t.Errorf("calls=%d waits=%d, want %d calls and %d waits", calls, waits, maxRetries+1, maxRetries)
	}
	if c.delay != DefaultDelay {
		t.Errorf("adaptive delay = %v, want unchanged %v", c.delay, DefaultDelay)
	}
}

type retryMethodCase struct {
	name string
	call func(context.Context, *Client) error
}

func retryMethodCases() []retryMethodCase {
	return []retryMethodCase{
		{name: "Get", call: func(ctx context.Context, c *Client) error { return c.Get(ctx, "/test", nil) }},
		{name: "GetRaw", call: func(ctx context.Context, c *Client) error { _, err := c.GetRaw(ctx, "/test"); return err }},
		{name: "GetRawURL", call: func(ctx context.Context, c *Client) error {
			_, err := c.GetRawURL(ctx, "https://example.test/test")
			return err
		}},
		{name: "Post", call: func(ctx context.Context, c *Client) error {
			return c.Post(ctx, "/test", map[string]string{"key": "value"}, nil)
		}},
	}
}

type trackingReadCloser struct {
	io.Reader
	closed *int
}

func (r *trackingReadCloser) Close() error {
	*r.closed++
	return nil
}

func TestGetRawURLIsolatesHeaders(t *testing.T) {
	var got http.Header
	// A TLS server so the URL is https, which GetRawURL requires.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("skill body"))
	}))
	defer srv.Close()

	// Client carries a session cookie that must NOT leak to the raw URL.
	c := &Client{
		http:    srv.Client(),
		baseURL: "https://www.perplexity.ai",
		delay:   0,
		cookies: []*http.Cookie{{Name: "__Secure-next-auth.session-token", Value: "secret"}},
	}

	body, err := c.GetRawURL(context.Background(), srv.URL+"/skill/SKILL.md")
	if err != nil {
		t.Fatalf("GetRawURL: %v", err)
	}
	if string(body) != "skill body" {
		t.Errorf("body = %q, want %q", string(body), "skill body")
	}

	// The Perplexity session cookie and API/Origin headers must be absent.
	forbidden := []string{"Cookie", "Origin", "Referer", "X-App-Apiclient", "X-App-Apiversion"}
	for _, h := range forbidden {
		if v := got.Get(h); v != "" {
			t.Errorf("GetRawURL leaked header %s = %q to third-party URL", h, v)
		}
	}
	// User-Agent is still sent (benign, needed by some CDNs).
	if got.Get("User-Agent") == "" {
		t.Error("GetRawURL should still send a User-Agent")
	}
}

func TestGetRawURLRejectsNonHTTPS(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: "https://www.perplexity.ai", delay: 0}

	// srv.URL is an http:// URL; other schemes and hostless URLs are also
	// refused. None of these should ever hit the network.
	cases := []string{srv.URL + "/skill.md", "file:///etc/passwd", "https:///no-host"}
	for _, raw := range cases {
		if _, err := c.GetRawURL(context.Background(), raw); err == nil {
			t.Errorf("GetRawURL(%q) = nil error, want rejection", raw)
		}
	}
	if reached {
		t.Error("GetRawURL issued a request for a rejected URL")
	}
}

func TestGetRawURLRejectsOversizedBody(t *testing.T) {
	// Serve one byte more than the cap; GetRawURL must error, not truncate.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, maxRawURLBody+1))
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: "https://www.perplexity.ai", delay: 0}

	if _, err := c.GetRawURL(context.Background(), srv.URL+"/big.md"); err == nil {
		t.Fatal("GetRawURL accepted an oversized body, want error")
	}
}

func TestNewClient(t *testing.T) {
	session := &models.SavedSession{
		SessionToken: "tok",
		Cookies: []models.Cookie{
			{Name: "test", Value: "val", Domain: ".example.com", Path: "/"},
		},
	}

	c, err := New(session)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != baseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, baseURL)
	}
	if c.delay != DefaultDelay {
		t.Errorf("delay = %v, want %v", c.delay, DefaultDelay)
	}
}
