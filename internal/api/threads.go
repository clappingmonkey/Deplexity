package api

import (
	"context"
	"fmt"
	"time"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

const apiVersion = "2.18"

// maxStalePages bounds how many consecutive detail pages may contain only
// already-seen entries before the fetch concludes the API is recycling
// results. Pages can legitimately overlap, so a single stale page is not
// treated as the end of a thread.
const maxStalePages = 3

type getter interface {
	Get(context.Context, string, any) error
}

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

// threadPagePath builds the detail endpoint URL for a single page of a thread.
func threadPagePath(uuid string, offset int) string {
	return fmt.Sprintf("/rest/thread/%s?with_schematized_response=true&version=%s&source=default&limit=50&offset=%d&from_first=true&supported_block_use_cases=answer_modes&supported_block_use_cases=preserve_latex", uuid, apiVersion, offset)
}

// toEntry converts a raw API thread entry into the exported model.
func toEntry(e ThreadEntry) models.Entry {
	entry := models.Entry{
		UUID:        e.UUID,
		Query:       e.QueryStr,
		Model:       e.DisplayModel,
		SearchFocus: e.SearchFocus,
		CreatedAt:   parseTime(e.CreatedAt),
	}

	// Extract markdown answer from blocks (prefer ask_text_0_markdown over ask_text).
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

	return entry
}

// GetThread fetches the full detail of a single thread including all entries.
//
// If resume is a previously persisted incomplete thread, fetching continues
// from its NextOffset instead of restarting from the beginning. onPage, when
// non-nil, is called after each fetched page so the caller can checkpoint
// progress; a page fetch failure leaves the last checkpoint on disk to be
// resumed by a later run.
func GetThread(ctx context.Context, c getter, uuid string, resume *models.Thread, onPage func(*models.Thread) error) (*models.Thread, error) {
	thread := &models.Thread{UUID: uuid, Slug: uuid}
	offset := 0
	seen := make(map[string]bool)
	resuming := false
	staleStreak := 0

	if resume != nil && !resume.Complete && resume.NextOffset > 0 {
		resuming = true
		thread.Title = resume.Title
		thread.CreatedAt = resume.CreatedAt
		thread.UpdatedAt = resume.UpdatedAt
		thread.SpaceUUID = resume.SpaceUUID
		thread.Bookmarked = resume.Bookmarked
		thread.Entries = append(thread.Entries, resume.Entries...)
		for _, e := range thread.Entries {
			seen[e.UUID] = true
		}
		offset = resume.NextOffset
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		var page ThreadDetailResponse
		if err := c.Get(ctx, threadPagePath(uuid, offset), &page); err != nil {
			return nil, fmt.Errorf("failed to get thread %s page at offset %d: %w", uuid, offset, err)
		}

		// A resumed offset past the end of the thread returns nothing, which
		// means the checkpoint is stale. Restart from the beginning rather
		// than marking a truncated thread complete. Only the first resumed
		// request can trigger this, so the restart cannot repeat.
		if resuming && len(page.Entries) == 0 {
			resuming = false
			thread = &models.Thread{UUID: uuid, Slug: uuid}
			seen = make(map[string]bool)
			offset = 0
			continue
		}
		resuming = false

		// Metadata only comes back reliably on the first page of a run.
		if thread.Title == "" {
			thread.Title = page.ThreadMetadata.Title
		}
		if thread.CreatedAt.IsZero() {
			thread.CreatedAt = parseTime(page.ThreadMetadata.CreatedAt)
		}
		if thread.UpdatedAt.IsZero() {
			thread.UpdatedAt = parseTime(page.ThreadMetadata.UpdatedAt)
		}

		// The API recycles results across pages, so skip entries already held.
		newCount := 0
		for _, e := range page.Entries {
			if seen[e.UUID] {
				continue
			}
			seen[e.UUID] = true
			newCount++
			thread.Entries = append(thread.Entries, toEntry(e))
		}

		if !page.HasNextPage || len(page.Entries) == 0 {
			break
		}

		// Pages can overlap, so a single page with nothing new does not prove
		// the thread has ended. Tolerate a bounded run of them before treating
		// the API as recycling, which also guarantees termination.
		if newCount == 0 {
			staleStreak++
			if staleStreak >= maxStalePages {
				break
			}
		} else {
			staleStreak = 0
		}

		offset += len(page.Entries)
		thread.NextOffset = offset
		if onPage != nil {
			if err := onPage(thread); err != nil {
				return nil, fmt.Errorf("failed to checkpoint thread %s at offset %d: %w", uuid, offset, err)
			}
		}
	}

	thread.Complete = true
	thread.NextOffset = 0
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
