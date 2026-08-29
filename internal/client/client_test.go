package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
