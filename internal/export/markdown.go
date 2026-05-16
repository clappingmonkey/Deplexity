package export

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// MarkdownExporter writes data as Markdown files.
type MarkdownExporter struct {
	OutputDir string
}

// ExportThread writes a single thread as a formatted Markdown file.
func (e *MarkdownExporter) ExportThread(thread *models.Thread) error {
	dir := e.threadDir(thread)
	return writeThreadMarkdown(dir, thread)
}

// writeThreadMarkdown writes a thread as Markdown into the given directory.
func writeThreadMarkdown(dir string, thread *models.Thread) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}

	var sb strings.Builder

	// Title
	title := thread.Title
	if title == "" {
		title = "Untitled Thread"
	}
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))

	// Metadata
	sb.WriteString(fmt.Sprintf("- **Thread ID:** %s\n", thread.UUID))
	if !thread.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- **Created:** %s\n", thread.CreatedAt.Format("2006-01-02 15:04:05 UTC")))
	}
	if !thread.UpdatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- **Updated:** %s\n", thread.UpdatedAt.Format("2006-01-02 15:04:05 UTC")))
	}
	if thread.SpaceUUID != "" {
		sb.WriteString(fmt.Sprintf("- **Space:** %s\n", thread.SpaceUUID))
	}
	sb.WriteString("\n---\n\n")

	// Entries (Q&A pairs)
	for i, entry := range thread.Entries {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}

		// Question
		sb.WriteString(fmt.Sprintf("## Q: %s\n\n", entry.Query))

		// Answer
		sb.WriteString(fmt.Sprintf("%s\n\n", entry.Answer))

		// Sources
		if len(entry.Sources) > 0 {
			sb.WriteString("### Sources\n\n")
			for j, source := range entry.Sources {
				title := source.Title
				if title == "" {
					title = source.URL
				}
				sb.WriteString(fmt.Sprintf("%d. [%s](%s)", j+1, title, source.URL))
				if source.Snippet != "" {
					sb.WriteString(fmt.Sprintf(" — %s", source.Snippet))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}

		// Model info
		if entry.Model != "" {
			sb.WriteString(fmt.Sprintf("*Model: %s*\n\n", entry.Model))
		}
	}

	path := filepath.Join(dir, "thread.md")
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("could not write markdown file: %w", err)
	}

	return nil
}

// ExportSpaces writes a summary Markdown file for all spaces and copies thread markdown into each space folder.
func (e *MarkdownExporter) ExportSpaces(ctx context.Context, spaces []models.Space, threads []models.Thread) error {
	dir := filepath.Join(e.OutputDir, "spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create spaces directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Perplexity Spaces\n\n")

	for _, space := range spaces {
		sb.WriteString(fmt.Sprintf("## %s\n\n", space.Name))
		if space.Description != "" {
			sb.WriteString(fmt.Sprintf("%s\n\n", space.Description))
		}
		if space.Instructions != "" {
			sb.WriteString(fmt.Sprintf("**AI Instructions:**\n\n> %s\n\n", space.Instructions))
		}
		sb.WriteString(fmt.Sprintf("- **UUID:** %s\n", space.UUID))
		if space.CreatedAt != nil {
			sb.WriteString(fmt.Sprintf("- **Created:** %s\n", space.CreatedAt.Format("2006-01-02 15:04:05 UTC")))
		}
		if len(space.ThreadUUIDs) > 0 {
			sb.WriteString(fmt.Sprintf("- **Threads:** %d\n", len(space.ThreadUUIDs)))
		}
		sb.WriteString("\n---\n\n")
	}

	path := filepath.Join(dir, "spaces.md")
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("could not write spaces markdown: %w", err)
	}

	// Copy thread markdown into each space folder.
	threadByUUID := make(map[string]*models.Thread, len(threads))
	for i := range threads {
		threadByUUID[threads[i].UUID] = &threads[i]
	}
	for _, space := range spaces {
		for _, uuid := range space.ThreadUUIDs {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			thread := threadByUUID[uuid]
			if thread == nil {
				continue
			}
			spaceDir := filepath.Join(dir, sanitizeFilename(space.Name))
			threadDir := filepath.Join(spaceDir, "threads", sanitizeFilename(threadSlug(thread)))
			if err := writeThreadMarkdown(threadDir, thread); err != nil {
				return err
			}
		}
	}

	return nil
}

// ExportUser writes the user profile as Markdown.
func (e *MarkdownExporter) ExportUser(user *models.User) error {
	dir := filepath.Join(e.OutputDir, "profile")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create profile directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Perplexity Profile\n\n")
	sb.WriteString(fmt.Sprintf("- **Name:** %s\n", user.Name))
	sb.WriteString(fmt.Sprintf("- **Email:** %s\n", user.Email))
	if user.Subscription != "" {
		sb.WriteString(fmt.Sprintf("- **Subscription:** %s\n", user.Subscription))
	}
	sb.WriteString(fmt.Sprintf("- **User ID:** %s\n", user.ID))

	path := filepath.Join(dir, "user.md")
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("could not write profile markdown: %w", err)
	}

	return nil
}

// threadDir returns the output directory for a thread.
func (e *MarkdownExporter) threadDir(thread *models.Thread) string {
	return filepath.Join(e.OutputDir, "threads", sanitizeFilename(threadSlug(thread)))
}
