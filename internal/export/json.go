package export

import (
	"context"
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

// SaveThreadIndex persists the lightweight thread UUID list for resumable export.
func (e *JSONExporter) SaveThreadIndex(index *models.ThreadIndex) error {
	if err := os.MkdirAll(e.OutputDir, 0755); err != nil {
		return fmt.Errorf("could not create output directory: %w", err)
	}
	return writeJSON(filepath.Join(e.OutputDir, "thread_index.json"), index)
}

// LoadThreadIndex reads the cached thread index from disk.
// Returns nil if the file does not exist.
func (e *JSONExporter) LoadThreadIndex() (*models.ThreadIndex, error) {
	path := filepath.Join(e.OutputDir, "thread_index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var index models.ThreadIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

// LoadCompleteThread reads a thread only when its detail fetch completed.
// Thread JSON written by earlier versions has no complete field and is retried.
func (e *JSONExporter) LoadCompleteThread(uuid string) (*models.Thread, error) {
	thread, err := e.LoadThread(uuid)
	if err != nil {
		return nil, err
	}
	if !thread.Complete {
		return nil, fmt.Errorf("thread %s is incomplete", uuid)
	}
	return thread, nil
}

// LoadThread reads a single thread from its JSON file on disk.
func (e *JSONExporter) LoadThread(uuid string) (*models.Thread, error) {
	path := filepath.Join(e.OutputDir, "threads", sanitizeFilename(uuid), "thread.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var thread models.Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		return nil, err
	}
	return &thread, nil
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

// ExportSpaces writes the spaces/collections data and copies thread files into each space folder.
func (e *JSONExporter) ExportSpaces(ctx context.Context, spaces []models.Space, threads []models.Thread) error {
	dir := filepath.Join(e.OutputDir, "spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create spaces directory: %w", err)
	}

	if err := writeJSON(filepath.Join(dir, "index.json"), spaces); err != nil {
		return err
	}

	// Build UUID -> thread lookup.
	threadByUUID := make(map[string]*models.Thread, len(threads))
	for i := range threads {
		threadByUUID[threads[i].UUID] = &threads[i]
	}

	for _, space := range spaces {
		spaceDir := filepath.Join(dir, sanitizeFilename(space.Name))
		if err := os.MkdirAll(spaceDir, 0755); err != nil {
			return fmt.Errorf("could not create space directory: %w", err)
		}
		if err := writeJSON(filepath.Join(spaceDir, "space.json"), space); err != nil {
			return err
		}

		// Copy thread JSON files into the space folder.
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
			threadDir := filepath.Join(spaceDir, "threads", sanitizeFilename(threadSlug(thread)))
			if err := os.MkdirAll(threadDir, 0755); err != nil {
				return fmt.Errorf("could not create space thread directory: %w", err)
			}
			if err := writeJSON(filepath.Join(threadDir, "thread.json"), thread); err != nil {
				return err
			}
			// Write sources separately.
			var allSources []models.Source
			for _, entry := range thread.Entries {
				allSources = append(allSources, entry.Sources...)
			}
			if len(allSources) > 0 {
				if err := writeJSON(filepath.Join(threadDir, "sources.json"), allSources); err != nil {
					return err
				}
			}
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
	return filepath.Join(e.OutputDir, "threads", sanitizeFilename(threadSlug(thread)))
}

// writeJSON writes data as indented JSON to a file atomically.
//
// Data is written to a temp file in the same directory and renamed over the
// target, so a crash mid-write cannot corrupt an existing file. This matters
// for thread.json, which is rewritten on every pagination checkpoint and must
// stay a valid resume point even if a later write fails.
func writeJSON(path string, data interface{}) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("could not create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we return before the rename succeeds.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)

	if err := enc.Encode(data); err != nil {
		tmp.Close()
		return fmt.Errorf("could not write JSON to %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("could not flush %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close temp file for %s: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("could not replace %s: %w", path, err)
	}
	tmpName = "" // rename succeeded; skip cleanup
	return nil
}
