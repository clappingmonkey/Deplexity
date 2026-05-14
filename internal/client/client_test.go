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
