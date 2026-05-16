package api

import (
	"context"
	"fmt"
	"time"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

const apiVersion = "2.18"

// ListThreadsFrom fetches threads starting at a given offset via POST /rest/thread/list_ask_threads.
// Stops when a page returns fewer than limit results (end of data) or when all results are
// duplicates (safety net for API recycling behavior).
func ListThreadsFrom(ctx context.Context, c *client.Client, startOffset int, seenUUIDs map[string]bool, onProgress func(int)) ([]models.Thread, error) {
	var allThreads []models.Thread
	offset := startOffset
	limit := 20

	path := fmt.Sprintf("/rest/thread/list_ask_threads?version=%s&source=default", apiVersion)

	for {
		select {
		case <-ctx.Done():
			return allThreads, ctx.Err()
		default:
		}

		reqBody := ThreadListRequest{
			Limit:         limit,
			Ascending:     false,
			Offset:        offset,
			SearchTerm:    "",
			ExcludeASI:    false,
			IncludeAssets: true,
		}

		var raw ThreadListResponse
		if err := c.Post(ctx, path, reqBody, &raw); err != nil {
			return allThreads, fmt.Errorf("failed to list threads: %w", err)
		}

		if len(raw) == 0 {
			break
		}

		newCount := 0
		for _, t := range raw {
			if seenUUIDs[t.UUID] {
				continue
			}
			seenUUIDs[t.UUID] = true
			newCount++

			thread := models.Thread{
				UUID:  t.UUID,
				Title: t.Title,
				Slug:  t.Slug,
			}
			if t.Collection != nil {
				thread.SpaceUUID = t.Collection.UUID
			}
			if t.LastQueryTime != "" {
				thread.UpdatedAt = parseTime(t.LastQueryTime)
			}
			allThreads = append(allThreads, thread)
		}

		if onProgress != nil {
			onProgress(startOffset + len(allThreads))
		}

		// If no new threads were found, the API is recycling — stop.
		if newCount == 0 {
			break
		}

		// If we got fewer than limit, we've reached the last page.
		if len(raw) < limit {
			break
		}

		offset += len(raw)
	}

	return allThreads, nil
}

// ListThreads fetches all user threads via POST /rest/thread/list_ask_threads.
// If onProgress is non-nil, it is called after each page with the total count so far.
func ListThreads(ctx context.Context, c *client.Client, onProgress func(int)) ([]models.Thread, error) {
	return ListThreadsFrom(ctx, c, 0, make(map[string]bool), onProgress)
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
