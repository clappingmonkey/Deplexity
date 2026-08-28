package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// GetUser fetches the authenticated user's profile.
func GetUser(ctx context.Context, c *client.Client) (*models.User, error) {
	var raw UserResponse
	if err := c.Get(ctx, "/api/user", &raw); err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	return &models.User{
		ID:           raw.ID,
		Email:        raw.Email,
		Name:         raw.Username,
		Subscription: raw.SubscriptionStatus,
	}, nil
}

// ValidateSession checks if the current session is valid by calling the auth endpoint.
//
// A hard 401/403 is mapped to client.ErrNotAuthenticated by the HTTP client. A
// half-valid session can instead return HTTP 200 with an empty user, so an
// authenticated body with no user identity is treated as unauthenticated too.
func ValidateSession(ctx context.Context, c *client.Client) (*SessionResponse, error) {
	var raw SessionResponse
	path := fmt.Sprintf("/api/auth/session?version=%s&source=default", apiVersion)
	if err := c.Get(ctx, path, &raw); err != nil {
		// Preserve the actionable "run 'deplexity login'" message unwrapped.
		if errors.Is(err, client.ErrNotAuthenticated) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to validate session: %w", err)
	}
	if !sessionIsAuthenticated(&raw) {
		return nil, client.ErrNotAuthenticated
	}
	return &raw, nil
}

// sessionIsAuthenticated reports whether a session response carries a real user
// identity. A half-valid session can return HTTP 200 with an empty user, which
// must be treated as unauthenticated.
func sessionIsAuthenticated(s *SessionResponse) bool {
	return s != nil && (s.User.ID != "" || s.User.Email != "")
}
