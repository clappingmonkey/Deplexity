package api

import (
	"context"
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
func ValidateSession(ctx context.Context, c *client.Client) (*SessionResponse, error) {
	var raw SessionResponse
	path := fmt.Sprintf("/api/auth/session?version=%s&source=default", apiVersion)
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("failed to validate session: %w", err)
	}
	return &raw, nil
}
