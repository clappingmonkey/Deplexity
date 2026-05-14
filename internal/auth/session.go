package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

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

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &SessionInfo{Valid: false}, nil
	}

	// NextAuth returns an empty object {} for invalid sessions.
	if len(result) == 0 {
		return &SessionInfo{Valid: false}, nil
	}

	info := &SessionInfo{Valid: true}

	if user, ok := result["user"].(map[string]interface{}); ok {
		if email, ok := user["email"].(string); ok {
			info.Email = email
		}
	}
	if expires, ok := result["expires"].(string); ok {
		if t, err := time.Parse(time.RFC3339, expires); err == nil {
			info.ExpiresAt = t
		}
	}

	return info, nil
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
