package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/api"
	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// stubCheckSession replaces the network check for one test.
func stubCheckSession(t *testing.T, fn func(*models.SavedSession) (*api.SessionResponse, error)) {
	t.Helper()
	orig := checkSession
	checkSession = func(_ context.Context, s *models.SavedSession) (*api.SessionResponse, error) { return fn(s) }
	t.Cleanup(func() { checkSession = orig })
}

func sessionResponse(id, email, expires string) *api.SessionResponse {
	r := &api.SessionResponse{Expires: expires}
	r.User.ID = id
	r.User.Email = email
	return r
}

func TestValidateSession(t *testing.T) {
	offset := time.FixedZone("x", 2*3600)
	tests := []struct {
		name        string
		resp        *api.SessionResponse
		err         error
		wantValid   bool
		wantErr     bool
		wantEmail   string
		wantExpires time.Time
	}{
		{name: "rejected", err: fmt.Errorf("wrapped: %w", client.ErrNotAuthenticated)},
		{name: "network failure", err: errors.New("dial tcp: timeout"), wantErr: true},
		{name: "empty user", resp: sessionResponse("", "", "")},
		{name: "user id", resp: sessionResponse("user-1", "", ""), wantValid: true},
		{name: "invalid expiry", resp: sessionResponse("user-1", "a@b.c", "not-a-date"), wantValid: true, wantEmail: "a@b.c"},
		{
			name:        "expiry converted to UTC",
			resp:        sessionResponse("user-1", "a@b.c", time.Date(2026, 9, 18, 14, 0, 0, 0, offset).Format(time.RFC3339)),
			wantValid:   true,
			wantEmail:   "a@b.c",
			wantExpires: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) { return tt.resp, tt.err })
			info, err := ValidateSession(context.Background(), &models.SavedSession{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if info.Valid != tt.wantValid || info.Email != tt.wantEmail || !info.ExpiresAt.Equal(tt.wantExpires) {
				t.Fatalf("info = %+v", info)
			}
			if !info.ExpiresAt.IsZero() && info.ExpiresAt.Location() != time.UTC {
				t.Fatalf("expiry location = %v, want UTC", info.ExpiresAt.Location())
			}
		})
	}
}

func TestValidateSessionRejectsNilSession(t *testing.T) {
	stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) {
		t.Fatal("nil session reached the network check")
		return nil, nil
	})
	info, err := ValidateSession(context.Background(), nil)
	if err != nil || info.Valid {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
}

func TestCookieLogin(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	t.Run("valid token takes the server expiry", func(t *testing.T) {
		var sent *models.SavedSession
		stubCheckSession(t, func(s *models.SavedSession) (*api.SessionResponse, error) {
			sent = s
			return sessionResponse("user-1", "a@b.c", future.Format(time.RFC3339)), nil
		})
		session, err := CookieLogin(context.Background(), "  tok-123\n")
		if err != nil {
			t.Fatal(err)
		}
		if sent == nil || sent.Cookies[0].Value != "tok-123" || session.SessionToken != "tok-123" {
			t.Fatalf("token not trimmed: sent=%+v session=%+v", sent, session)
		}
		if !session.ExpiresAt.Equal(future) || session.Cookies[0].Expires != float64(future.Unix()) {
			t.Fatalf("expiry = %v / %v, want %v", session.ExpiresAt, session.Cookies[0].Expires, future)
		}
	})

	t.Run("past server expiry keeps the fallback", func(t *testing.T) {
		stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) {
			return sessionResponse("user-1", "", time.Now().Add(-time.Hour).Format(time.RFC3339)), nil
		})
		session, err := CookieLogin(context.Background(), "tok")
		if err != nil {
			t.Fatal(err)
		}
		if !session.ExpiresAt.After(time.Now()) || session.Cookies[0].Expires != 0 {
			t.Fatalf("expiry = %v / %v", session.ExpiresAt, session.Cookies[0].Expires)
		}
	})

	t.Run("rejected token", func(t *testing.T) {
		stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) { return nil, client.ErrNotAuthenticated })
		if _, err := CookieLogin(context.Background(), "tok"); !errors.Is(err, ErrSessionRejected) || !strings.Contains(err.Error(), "invalid or expired token") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("network failure", func(t *testing.T) {
		stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) { return nil, errors.New("dial failed") })
		if _, err := CookieLogin(context.Background(), "tok"); err == nil || !strings.Contains(err.Error(), "dial failed") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("malformed token never reaches the network", func(t *testing.T) {
		stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) {
			t.Fatal("malformed token was sent")
			return nil, nil
		})
		for _, tok := range []string{"", "   ", "a;b=c", "a b", "a\"b", "a\r\nX-Evil: 1", "a,b", "a\\b", strings.Repeat("x", maxTokenLength+1)} {
			if _, err := CookieLogin(context.Background(), tok); err == nil {
				t.Errorf("CookieLogin(%q) succeeded", tok)
			}
		}
	})
}

func TestConfirmSessionSetsOnlySessionCookieExpiry(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	stubCheckSession(t, func(*models.SavedSession) (*api.SessionResponse, error) {
		return sessionResponse("u", "", future.Format(time.RFC3339)), nil
	})
	session := &models.SavedSession{Cookies: []models.Cookie{
		{Name: csrfCookieName, Value: "c"},
		{Name: sessionCookieName, Value: "s"},
	}}
	if _, err := ConfirmSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if session.Cookies[0].Expires != 0 || session.Cookies[1].Expires != float64(future.Unix()) {
		t.Fatalf("cookies = %+v", session.Cookies)
	}
}

func TestReadToken(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "line", in: "tok-1\n", want: "tok-1"},
		{name: "crlf", in: "tok-1\r\n", want: "tok-1"},
		{name: "no newline", in: "tok-1", want: "tok-1"},
		{name: "only first line", in: "tok-1\nsecond\n", want: "tok-1"},
		{name: "empty", in: "", wantErr: true},
		{name: "blank line", in: "\n", wantErr: true},
		{name: "exactly max", in: strings.Repeat("x", maxTokenLength) + "\n", want: strings.Repeat("x", maxTokenLength)},
		{name: "too long", in: strings.Repeat("x", maxTokenLength+1), wantErr: true},
		{name: "crlf", in: "tok\r\n", want: "tok"},
		{name: "limit hides the rest of the line", in: "good" + strings.Repeat("\r", maxTokenLength-2) + ";invalid\n", wantErr: true},
		{name: "huge", in: strings.Repeat("x", 1<<20), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadToken(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("ReadToken = %q, %v", got, err)
			}
		})
	}
}
