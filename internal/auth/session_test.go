package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDecodeSessionInfo(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantValid   bool
		wantEmail   string
		wantExpires time.Time
	}{
		{name: "empty object", body: `{}`},
		{name: "empty body", body: ``},
		{name: "null body", body: `null`},
		{name: "empty user", body: `{"user":{}}`},
		{name: "unrelated fields", body: `{"expires":"2026-09-18T12:00:00Z"}`},
		{name: "malformed JSON", body: `{"user":`},
		{name: "trailing JSON", body: `{"user":{"id":"user-1"}}{"extra":true}`},
		{name: "trailing garbage", body: `{"user":{"id":"user-1"}} garbage`},
		{name: "trailing whitespace", body: "{\"user\":{\"id\":\"user-1\"}}\n \t", wantValid: true},
		{name: "user id", body: `{"user":{"id":"user-1"}}`, wantValid: true},
		{name: "user email", body: `{"user":{"email":"user@example.com"}}`, wantValid: true, wantEmail: "user@example.com"},
		{
			name:        "valid expiry",
			body:        `{"user":{"id":"user-1","email":"user@example.com"},"expires":"2026-09-18T12:00:00Z"}`,
			wantValid:   true,
			wantEmail:   "user@example.com",
			wantExpires: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		},
		{name: "invalid expiry", body: `{"user":{"id":"user-1"},"expires":"not-a-date"}`, wantValid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := decodeSessionInfo(strings.NewReader(tt.body))
			if info.Valid != tt.wantValid {
				t.Errorf("Valid = %t, want %t", info.Valid, tt.wantValid)
			}
			if info.Email != tt.wantEmail {
				t.Errorf("Email = %q, want %q", info.Email, tt.wantEmail)
			}
			if !info.ExpiresAt.Equal(tt.wantExpires) {
				t.Errorf("ExpiresAt = %v, want %v", info.ExpiresAt, tt.wantExpires)
			}
		})
	}
}

func TestValidateSessionRejectsNilSession(t *testing.T) {
	info, err := ValidateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if info.Valid {
		t.Fatal("nil session was marked valid")
	}
}
