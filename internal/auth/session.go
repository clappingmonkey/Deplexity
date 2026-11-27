package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/clappingmonkey/deplexity/internal/api"
	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// maxTokenLength bounds a session token read from --cookie, the environment
// or stdin. Browsers cap a whole cookie at 4096 bytes.
const maxTokenLength = 4096

// SessionInfo holds the result of validating a session.
type SessionInfo struct {
	Valid     bool
	Email     string
	ExpiresAt time.Time
}

// checkSession asks Perplexity whether a session is signed in. It is a
// variable so tests can stub the network call.
var checkSession = func(ctx context.Context, session *models.SavedSession) (*api.SessionResponse, error) {
	c, err := client.New(session)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return api.ValidateSession(ctx, c)
}

// ValidateSession checks whether a saved session is still signed in, using
// the same client (TLS fingerprint, headers, retries) as the export. A
// rejected session is reported as Valid=false; a request that could not be
// completed is returned as an error.
func ValidateSession(ctx context.Context, session *models.SavedSession) (*SessionInfo, error) {
	if session == nil {
		return &SessionInfo{Valid: false}, nil
	}
	raw, err := checkSession(ctx, session)
	if err != nil {
		if errors.Is(err, client.ErrNotAuthenticated) {
			return &SessionInfo{Valid: false}, nil
		}
		return nil, err
	}
	return sessionInfo(raw), nil
}

func sessionInfo(raw *api.SessionResponse) *SessionInfo {
	if !api.SessionIsAuthenticated(raw) {
		return &SessionInfo{Valid: false}
	}
	info := &SessionInfo{Valid: true, Email: raw.User.Email}
	if expires, err := time.Parse(time.RFC3339, raw.Expires); err == nil {
		info.ExpiresAt = expires.UTC()
	}
	return info
}

// ErrSessionRejected reports a session Perplexity does not accept.
var ErrSessionRejected = errors.New("Perplexity rejected the session — please log in again")

// ConfirmSession validates a freshly captured session and records the expiry
// the server reports for it, unless that is already past (e.g. clock skew).
func ConfirmSession(ctx context.Context, session *models.SavedSession) (*SessionInfo, error) {
	info, err := ValidateSession(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("could not validate session: %w", err)
	}
	if !info.Valid {
		return nil, ErrSessionRejected
	}
	if info.ExpiresAt.After(time.Now()) {
		session.ExpiresAt = info.ExpiresAt
		for i := range session.Cookies {
			if session.Cookies[i].Name == sessionCookieName {
				session.Cookies[i].Expires = float64(info.ExpiresAt.UnixMilli()) / 1000
			}
		}
	}
	return info, nil
}

// CookieLogin creates a session from a manually provided session token,
// bypassing browser-based authentication entirely.
func CookieLogin(ctx context.Context, token string) (*models.SavedSession, error) {
	token, err := normalizeToken(token)
	if err != nil {
		return nil, err
	}
	session := &models.SavedSession{
		SessionToken: token,
		Cookies: []models.Cookie{
			{
				Name:     sessionCookieName,
				Value:    token,
				Domain:   "." + perplexityDomain,
				Path:     "/",
				Secure:   true,
				HTTPOnly: true,
			},
		},
		ExpiresAt: time.Now().Add(sessionFallbackTTL),
	}

	fmt.Println("Validating session token...")
	info, err := ConfirmSession(ctx, session)
	if errors.Is(err, ErrSessionRejected) {
		return nil, fmt.Errorf("invalid or expired token — please check the value and try again: %w", err)
	}
	if err != nil {
		return nil, err
	}
	if info.Email != "" {
		fmt.Printf("Token valid for: %s\n", info.Email)
	}
	return session, nil
}

// ReadToken reads a session token from r: the first line, without
// surrounding whitespace, at most maxTokenLength bytes.
func ReadToken(r io.Reader) (string, error) {
	// Read one byte past the longest allowed line ("token\r\n"), so a line cut
	// off by the limit is reported as too long instead of read as a token.
	const limit = maxTokenLength + 2
	line, err := bufio.NewReaderSize(io.LimitReader(r, limit+1), limit+1).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("could not read session token: %w", err)
	}
	complete := strings.HasSuffix(line, "\n") || len(line) <= limit
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !complete || len(line) > maxTokenLength {
		return "", fmt.Errorf("session token is longer than %d bytes", maxTokenLength)
	}
	return normalizeToken(line)
}

// normalizeToken trims whitespace and rejects values that are not a valid
// cookie value, so a pasted token cannot add other cookies or headers.
func normalizeToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("session token is empty")
	}
	if len(token) > maxTokenLength {
		return "", fmt.Errorf("session token is longer than %d bytes", maxTokenLength)
	}
	for i := 0; i < len(token); i++ {
		if !isCookieOctet(token[i]) {
			return "", errors.New("session token contains characters that are not allowed in a cookie (copy only the value of " + sessionCookieName + ")")
		}
	}
	return token, nil
}

// isCookieOctet reports whether b may appear in an unquoted cookie value
// (RFC 6265 cookie-octet): printable ASCII except space, '"', ',', ';' and
// '\'.
func isCookieOctet(b byte) bool {
	return b > 0x20 && b < 0x7f && b != '"' && b != ',' && b != ';' && b != '\\'
}
