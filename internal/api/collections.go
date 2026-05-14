package api

import (
	"context"
	"fmt"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// ListCollections fetches all user spaces/collections.
// TODO: The endpoint for collections in the new API is unknown.
// Previously /rest/collections/, now likely something else.
func ListCollections(ctx context.Context, c *client.Client) ([]models.Space, error) {
	_ = c
	return nil, fmt.Errorf("collections endpoint not yet discovered — check DevTools for the correct path")
}
