package api

import (
	"fmt"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// ListCollections fetches all user spaces/collections.
func ListCollections(c *client.Client) ([]models.Space, error) {
	var raw CollectionListResponse
	if err := c.Get("/rest/collections/", &raw); err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}

	spaces := make([]models.Space, 0, len(raw))
	for _, col := range raw {
		spaces = append(spaces, models.Space{
			UUID:         col.UUID,
			Name:         col.Name,
			Description:  col.Description,
			Instructions: col.Instructions,
			CreatedAt:    parseTime(col.CreatedAt),
			UpdatedAt:    parseTime(col.UpdatedAt),
			ThreadUUIDs:  col.ThreadUUIDs,
		})
	}

	return spaces, nil
}
