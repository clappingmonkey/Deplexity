package api

import (
	"context"
	"fmt"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// ListCollections fetches all user spaces/collections via GET /rest/spaces.
func ListCollections(ctx context.Context, c *client.Client) ([]models.Space, error) {
	var raw SpacesResponse
	path := fmt.Sprintf("/rest/spaces?version=%s&source=default", apiVersion)
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("failed to list spaces: %w", err)
	}

	var spaces []models.Space

	// Combine all space categories into a single list.
	allItems := make([]SpaceItem, 0)
	allItems = append(allItems, raw.PrivateSpaces...)
	allItems = append(allItems, raw.SharedSpaces...)
	allItems = append(allItems, raw.InvitedSpaces...)
	allItems = append(allItems, raw.SavedSpaces...)
	allItems = append(allItems, raw.OrganizationSpaces...)

	for _, item := range allItems {
		spaces = append(spaces, models.Space{
			UUID:      item.UUID,
			Name:      item.Title,
			Slug:      item.Slug,
			Emoji:     item.Emoji,
			UpdatedAt: parseTime(item.Updated),
		})
	}

	return spaces, nil
}
