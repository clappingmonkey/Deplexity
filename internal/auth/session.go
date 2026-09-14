package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/clappingmonkey/deplexity/internal/api"
	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

const sessionValidateURL = "https://www.perplexity.ai/api/auth/session"

// SessionInfo holds the result of validating a session.
type SessionInfo struct {
	Valid     bool
	Email     string
	ExpiresAt time.Time
}

// ValidateSession checks if the saved session is still valid by calling
// Perplexity's NextAuth session endpoint.
func ValidateSession(ctx context.Context, session *models.SavedSession) (*SessionInfo, error) {
	if session == nil {
		return &SessionInfo{Valid: false}, nil
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("could not create cookie jar: %w", err)
	}

	perplexityURL, _ := url.Parse("https://www.perplexity.ai")
	var httpCookies []*http.Cookie
	for _, c := range session.Cookies {
		httpCookies = append(httpCookies, &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		})
	}
	jar.SetCookies(perplexityURL, httpCookies)

	httpClient := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", sessionValidateURL, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}
	req.Header.Set("User-Agent", client.UserAgent)
	req.Header.Set("Referer", "https://www.perplexity.ai/")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("session validation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &SessionInfo{Valid: false}, nil
	}

	return decodeSessionInfo(resp.Body), nil
}

func decodeSessionInfo(r io.Reader) *SessionInfo {
	if r == nil {
		return &SessionInfo{Valid: false}
	}

	var result api.SessionResponse
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&result); err != nil {
		return &SessionInfo{Valid: false}
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF || !api.SessionIsAuthenticated(&result) {
		return &SessionInfo{Valid: false}
	}

	info := &SessionInfo{Valid: true, Email: result.User.Email}
	if expires, err := time.Parse(time.RFC3339, result.Expires); err == nil {
		info.ExpiresAt = expires
	}
	return info
}

// CookieLogin creates a session from a manually provided session token,
// bypassing browser-based authentication entirely.
func CookieLogin(ctx context.Context, token string) (*models.SavedSession, error) {
	session := &models.SavedSession{
		SessionToken: token,
		Cookies: []models.Cookie{
			{
				Name:     "__Secure-next-auth.session-token",
				Value:    token,
				Domain:   ".perplexity.ai",
				Path:     "/",
				Secure:   true,
				HTTPOnly: true,
			},
		},
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}

	fmt.Println("Validating session token...")
	info, err := ValidateSession(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("could not validate token: %w", err)
	}
	if !info.Valid {
		return nil, fmt.Errorf("invalid or expired token — please check the value and try again")
	}

	fmt.Printf("Token valid for: %s\n", info.Email)
	return session, nil
}
