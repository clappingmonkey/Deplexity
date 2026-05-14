package api

import (
	"fmt"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// GetUser fetches the authenticated user's profile.
func GetUser(c *client.Client) (*models.User, error) {
	var raw UserResponse
	if err := c.Get("/rest/user/", &raw); err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	return &models.User{
		ID:           raw.ID,
		Email:        raw.Email,
		Name:         raw.Name,
		ImageURL:     raw.ImageURL,
		Subscription: raw.Subscription,
	}, nil
}

// GetRateLimit fetches the current rate limit status.
func GetRateLimit(c *client.Client) (*RateLimitResponse, error) {
	var raw RateLimitResponse
	if err := c.Get("/rest/rate-limit/all", &raw); err != nil {
		return nil, fmt.Errorf("failed to get rate limits: %w", err)
	}
	return &raw, nil
}
