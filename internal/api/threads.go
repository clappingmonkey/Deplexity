package api

import (
	"context"
	"fmt"
	"time"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

const apiVersion = "2.18"

// ListThreads fetches all user threads, paginating through list_recent.
// If onProgress is non-nil, it is called after each page with the total count so far.
func ListThreads(ctx context.Context, c *client.Client, onProgress func(int)) ([]models.Thread, error) {
	var allThreads []models.Thread
	offset := 0
	limit := 20 // API caps at 20 per page regardless of requested limit

	for {
		select {
		case <-ctx.Done():
			return allThreads, ctx.Err()
		default:
		}

		var raw ThreadListResponse
		path := fmt.Sprintf("/rest/thread/list_recent?exclude_asi=false&version=%s&source=default&limit=%d&offset=%d", apiVersion, limit, offset)
		if err := c.Get(ctx, path, &raw); err != nil {
			return nil, fmt.Errorf("failed to list threads: %w", err)
		}

		if len(raw) == 0 {
			break
		}

		for _, t := range raw {
			allThreads = append(allThreads, models.Thread{
				UUID:       t.UUID,
				Title:      t.Title,
				Slug:       t.UUID,
				Bookmarked: false,
			})
		}

		if onProgress != nil {
			onProgress(len(allThreads))
		}

		// If we got fewer than limit, we've reached the end.
		if len(raw) < limit {
			break
		}
		offset += len(raw)
	}

	return allThreads, nil
}

// GetThread fetches the full detail of a single thread including all entries.
func GetThread(ctx context.Context, c *client.Client, uuid string) (*models.Thread, error) {
	var raw ThreadDetailResponse
	path := fmt.Sprintf("/rest/thread/%s?with_schematized_response=true&version=%s&source=default&limit=50&offset=0&from_first=true&supported_block_use_cases=answer_modes&supported_block_use_cases=preserve_latex", uuid, apiVersion)
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("failed to get thread %s: %w", uuid, err)
	}

	thread := &models.Thread{
		UUID:      uuid,
		Slug:      uuid,
		Title:     raw.ThreadMetadata.Title,
		CreatedAt: parseTime(raw.ThreadMetadata.CreatedAt),
		UpdatedAt: parseTime(raw.ThreadMetadata.UpdatedAt),
	}

	for _, e := range raw.Entries {
		entry := models.Entry{
			UUID:        e.UUID,
			Query:       e.QueryStr,
			Model:       e.DisplayModel,
			SearchFocus: e.SearchFocus,
			CreatedAt:   parseTime(e.CreatedAt),
		}

		// Extract markdown answer from blocks (prefer ask_text_0_markdown over ask_text).
		for _, b := range e.Blocks {
			if b.MarkdownBlock != nil && b.IntendedUsage == "ask_text_0_markdown" {
				if b.MarkdownBlock.Answer != "" {
					entry.Answer = b.MarkdownBlock.Answer
					break
				}
			}
		}
		if entry.Answer == "" {
			for _, b := range e.Blocks {
				if b.MarkdownBlock != nil && b.IntendedUsage == "ask_text" {
					if b.MarkdownBlock.Answer != "" {
						entry.Answer = b.MarkdownBlock.Answer
						break
					}
				}
			}
		}

		// Extract sources from web_results block.
		for _, b := range e.Blocks {
			if b.WebResultBlock != nil && b.IntendedUsage == "web_results" {
				for _, wr := range b.WebResultBlock.WebResults {
					entry.Sources = append(entry.Sources, models.Source{
						Title:   wr.Name,
						URL:     wr.URL,
						Snippet: wr.Snippet,
					})
				}
				break
			}
		}

		thread.Entries = append(thread.Entries, entry)
	}

	// Fetch remaining pages if needed.
	if raw.HasNextPage {
		offset := len(raw.Entries)
		for {
			var page ThreadDetailResponse
			pagePath := fmt.Sprintf("/rest/thread/%s?with_schematized_response=true&version=%s&source=default&limit=50&offset=%d&from_first=true&supported_block_use_cases=answer_modes&supported_block_use_cases=preserve_latex", uuid, apiVersion, offset)
			if err := c.Get(ctx, pagePath, &page); err != nil {
				break // best effort
			}
			for _, e := range page.Entries {
				entry := models.Entry{
					UUID:        e.UUID,
					Query:       e.QueryStr,
					Model:       e.DisplayModel,
					SearchFocus: e.SearchFocus,
					CreatedAt:   parseTime(e.CreatedAt),
				}
				for _, b := range e.Blocks {
					if b.MarkdownBlock != nil && b.IntendedUsage == "ask_text_0_markdown" && b.MarkdownBlock.Answer != "" {
						entry.Answer = b.MarkdownBlock.Answer
						break
					}
				}
				if entry.Answer == "" {
					for _, b := range e.Blocks {
						if b.MarkdownBlock != nil && b.IntendedUsage == "ask_text" && b.MarkdownBlock.Answer != "" {
							entry.Answer = b.MarkdownBlock.Answer
							break
						}
					}
				}
				for _, b := range e.Blocks {
					if b.WebResultBlock != nil && b.IntendedUsage == "web_results" {
						for _, wr := range b.WebResultBlock.WebResults {
							entry.Sources = append(entry.Sources, models.Source{
								Title:   wr.Name,
								URL:     wr.URL,
								Snippet: wr.Snippet,
							})
						}
						break
					}
				}
				thread.Entries = append(thread.Entries, entry)
			}
			if !page.HasNextPage {
				break
			}
			offset += len(page.Entries)
		}
	}

	return thread, nil
}

// parseTime attempts to parse a time string in multiple formats.
func parseTime(s string) time.Time {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000000+00:00",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}

	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}

	return time.Time{}
}
