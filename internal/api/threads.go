package api

import (
	"context"
	"fmt"
	"time"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// ListThreads fetches the list of all user threads.
func ListThreads(ctx context.Context, c *client.Client) ([]models.Thread, error) {
	var raw ThreadListResponse
	if err := c.Get(ctx, "/rest/threads/", &raw); err != nil {
		return nil, fmt.Errorf("failed to list threads: %w", err)
	}

	threads := make([]models.Thread, 0, len(raw))
	for _, t := range raw {
		threads = append(threads, models.Thread{
			UUID:       t.UUID,
			Slug:       t.Slug,
			Title:      t.Title,
			CreatedAt:  parseTime(t.CreatedAt),
			UpdatedAt:  parseTime(t.UpdatedAt),
			SpaceUUID:  t.SpaceUUID,
			Bookmarked: t.Bookmarked,
		})
	}

	return threads, nil
}

// GetThread fetches the full detail of a single thread including all entries.
func GetThread(ctx context.Context, c *client.Client, uuid string) (*models.Thread, error) {
	var raw ThreadDetailResponse
	path := fmt.Sprintf("/rest/threads/%s", uuid)
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("failed to get thread %s: %w", uuid, err)
	}

	thread := &models.Thread{
		UUID:      raw.UUID,
		Slug:      raw.Slug,
		Title:     raw.Title,
		CreatedAt: parseTime(raw.CreatedAt),
		UpdatedAt: parseTime(raw.UpdatedAt),
		SpaceUUID: raw.SpaceUUID,
	}

	for _, e := range raw.Entries {
		entry := models.Entry{
			UUID:        e.UUID,
			Query:       e.Query,
			Answer:      e.Answer,
			Model:       e.Model,
			SearchFocus: e.SearchFocus,
			CreatedAt:   parseTime(e.CreatedAt),
		}

		for _, s := range e.Sources {
			entry.Sources = append(entry.Sources, models.Source{
				Title:   s.Title,
				URL:     s.URL,
				Snippet: s.Snippet,
				Favicon: s.Favicon,
			})
		}

		thread.Entries = append(thread.Entries, entry)
	}

	return thread, nil
}

// parseTime attempts to parse a time string in multiple formats.
func parseTime(s string) time.Time {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	}

	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}

	return time.Time{}
}
