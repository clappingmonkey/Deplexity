package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// JSONExporter writes data as formatted JSON files.
type JSONExporter struct {
	OutputDir string
}

// ExportThread writes a single thread as a JSON file.
func (e *JSONExporter) ExportThread(thread *models.Thread) error {
	dir := e.threadDir(thread)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}

	// Write thread data
	if err := writeJSON(filepath.Join(dir, "thread.json"), thread); err != nil {
		return err
	}

	// Write sources separately for easy access
	var allSources []models.Source
	for _, entry := range thread.Entries {
		allSources = append(allSources, entry.Sources...)
	}
	if len(allSources) > 0 {
		if err := writeJSON(filepath.Join(dir, "sources.json"), allSources); err != nil {
			return err
		}
	}

	return nil
}

// ExportThreadIndex writes the thread listing index.
func (e *JSONExporter) ExportThreadIndex(threads []models.Thread) error {
	dir := filepath.Join(e.OutputDir, "threads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create threads directory: %w", err)
	}

	// Strip entries for the index — just metadata
	type threadMeta struct {
		UUID       string `json:"uuid"`
		Slug       string `json:"slug"`
		Title      string `json:"title"`
		CreatedAt  string `json:"created_at"`
		UpdatedAt  string `json:"updated_at"`
		SpaceUUID  string `json:"space_uuid,omitempty"`
		Bookmarked bool   `json:"bookmarked,omitempty"`
		EntryCount int    `json:"entry_count"`
	}

	index := make([]threadMeta, 0, len(threads))
	for _, t := range threads {
		index = append(index, threadMeta{
			UUID:       t.UUID,
			Slug:       t.Slug,
			Title:      t.Title,
			CreatedAt:  t.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:  t.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			SpaceUUID:  t.SpaceUUID,
			Bookmarked: t.Bookmarked,
			EntryCount: len(t.Entries),
		})
	}

	return writeJSON(filepath.Join(dir, "index.json"), index)
}

// ExportSpaces writes the spaces/collections data.
func (e *JSONExporter) ExportSpaces(spaces []models.Space) error {
	dir := filepath.Join(e.OutputDir, "spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create spaces directory: %w", err)
	}

	if err := writeJSON(filepath.Join(dir, "index.json"), spaces); err != nil {
		return err
	}

	for _, space := range spaces {
		spaceDir := filepath.Join(dir, sanitizeFilename(space.Name))
		if err := os.MkdirAll(spaceDir, 0755); err != nil {
			return fmt.Errorf("could not create space directory: %w", err)
		}
		if err := writeJSON(filepath.Join(spaceDir, "space.json"), space); err != nil {
			return err
		}
	}

	return nil
}

// ExportUser writes the user profile data.
func (e *JSONExporter) ExportUser(user *models.User) error {
	dir := filepath.Join(e.OutputDir, "profile")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create profile directory: %w", err)
	}
	return writeJSON(filepath.Join(dir, "user.json"), user)
}

// ExportManifest writes the export manifest.
func (e *JSONExporter) ExportManifest(manifest *models.ExportManifest) error {
	return writeJSON(filepath.Join(e.OutputDir, "manifest.json"), manifest)
}

// threadDir returns the output directory for a thread.
func (e *JSONExporter) threadDir(thread *models.Thread) string {
	slug := thread.Slug
	if slug == "" {
		slug = thread.UUID
	}
	return filepath.Join(e.OutputDir, "threads", sanitizeFilename(slug))
}

// writeJSON writes data as indented JSON to a file.
func writeJSON(path string, data interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("could not create %s: %w", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)

	if err := enc.Encode(data); err != nil {
		return fmt.Errorf("could not write JSON to %s: %w", path, err)
	}

	return nil
}
