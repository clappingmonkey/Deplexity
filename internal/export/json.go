package export

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const threadCacheVersion = 1

// JSONExporter writes data as formatted JSON files.
type JSONExporter struct {
	OutputDir string
}

// SaveThreadIndex persists the lightweight thread UUID list for resumable export.
func (e *JSONExporter) SaveThreadIndex(index *models.ThreadIndex) error {
	if err := os.MkdirAll(e.OutputDir, 0755); err != nil {
		return fmt.Errorf("could not create output directory: %w", err)
	}
	index.Version = models.ThreadIndexVersion
	return writeJSON(filepath.Join(e.OutputDir, "thread_index.json"), index)
}

// LoadThreadIndex reads the cached thread index from disk.
// Returns nil if the file does not exist.
//
// An index written before fetch watermarks existed is migrated once: each
// thread's detail is assumed to sit under its slug, but its watermark stays
// unknown. An interrupted --refresh on those versions could save a listing
// time newer than the detail on disk, so that time is not trusted; the next
// --refresh fetches every thread once. Plain runs are unaffected.
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
	if index.Version < 1 {
		for i := range index.Threads {
			ref := &index.Threads[i]
			if ref.CacheSlug == "" {
				ref.CacheSlug = ref.Slug
			}
		}
		index.Version = models.ThreadIndexVersion
	}
	return &index, nil
}

// LoadCompleteThread reads a thread only when its detail fetch completed.
// Thread JSON written by earlier versions has no complete field and is retried.
func (e *JSONExporter) LoadCompleteThread(ref models.ThreadRef) (*models.Thread, error) {
	thread, err := e.LoadThread(ref)
	if err != nil {
		return nil, err
	}
	if !thread.Complete {
		return nil, fmt.Errorf("thread %s is incomplete", ref.UUID)
	}
	return thread, nil
}

func threadDirPath(outputDir, slug, uuid string) string {
	return filepath.Join(outputDir, "threads", threadDirName(slug, uuid), "thread.json")
}

// LoadThread reads a single thread from its JSON file on disk.
func (e *JSONExporter) LoadThread(ref models.ThreadRef) (*models.Thread, error) {
	// The slug the latest detail was written under comes first: a thread
	// renamed back to an older slug can still have a stale copy there. Then
	// the current slug, and the slug from the previous index.
	var paths []string
	for _, slug := range []string{ref.CacheSlug, ref.Slug, ref.PreviousSlug} {
		if p := threadDirPath(e.OutputDir, slug, ref.UUID); slug != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	// Legacy UUID-only cache paths.
	for _, p := range []string{
		threadDirPath(e.OutputDir, "", ref.UUID),
		filepath.Join(e.OutputDir, "threads", sanitizeFilename(ref.UUID), "thread.json"),
	} {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	var lastErr error
	for _, threadPath := range paths {
		data, err := os.ReadFile(threadPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
			if lastErr == nil {
				lastErr = err
			}
			continue
		}
		var thread models.Thread
		if err := json.Unmarshal(data, &thread); err != nil {
			return nil, err
		}
		if thread.UUID != ref.UUID {
			lastErr = fmt.Errorf("thread cache %s contains UUID %q, want %q", threadPath, thread.UUID, ref.UUID)
			continue
		}
		if thread.CacheVersion != threadCacheVersion {
			lastErr = fmt.Errorf("thread cache %s has version %d, want %d", threadPath, thread.CacheVersion, threadCacheVersion)
			continue
		}
		return &thread, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("thread %s was not found in a matching cache: %w", ref.UUID, lastErr)
	}
	return nil, fmt.Errorf("thread %s has no cache paths", ref.UUID)
}

// HasCanonicalThread reports whether the current slug-and-UUID cache path exists.
func (e *JSONExporter) HasCanonicalThread(ref models.ThreadRef) bool {
	_, err := os.Stat(filepath.Join(e.OutputDir, "threads", threadDirName(ref.Slug, ref.UUID), "thread.json"))
	return err == nil
}

// ExportThread writes a single thread as a JSON file.
func (e *JSONExporter) ExportThread(thread *models.Thread) error {
	dir := e.threadDir(thread)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}

	// A complete thread.json is the cache validity marker. Write an incomplete
	// copy first so a later sidecar failure cannot leave an older complete cache
	// looking valid, then write the complete marker only after all sidecars.
	threadPath := filepath.Join(dir, "thread.json")
	current := *thread
	current.CacheVersion = threadCacheVersion
	if current.Complete {
		incomplete := current
		incomplete.Complete = false
		if err := writeJSON(threadPath, &incomplete); err != nil {
			return err
		}
	} else if err := writeJSON(threadPath, &current); err != nil {
		return err
	}

	if err := writeThreadSources(dir, thread); err != nil {
		return err
	}
	if current.Complete {
		if err := writeJSON(threadPath, &current); err != nil {
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
			CreatedAt:  t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:  t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
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
	spaceDirs := spaceDirNames(spaces)

	// Iterate by index (&spaces[i]) rather than a range copy: writeSpaceSkills
	// records each skill's BodyFile in place, and that mutation must be visible
	// to the space.json serialization below.
	for i := range spaces {
		space := &spaces[i]
		spaceDir := filepath.Join(dir, spaceDirs[i])
		if err := os.MkdirAll(spaceDir, 0755); err != nil {
			return fmt.Errorf("could not create space directory: %w", err)
		}

		// Write skill bodies as sidecar SKILL.md files and record their
		// relative paths so space.json references them. Done before writing
		// space.json so the body_file field is populated.
		if err := e.writeSpaceSkills(spaceDir, space.Skills); err != nil {
			return err
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
			threadDir := filepath.Join(spaceDir, "threads", threadDirName(thread.Slug, thread.UUID))
			if err := os.MkdirAll(threadDir, 0755); err != nil {
				return fmt.Errorf("could not create space thread directory: %w", err)
			}
			if err := writeThreadSources(threadDir, thread); err != nil {
				return err
			}
			if err := writeJSON(filepath.Join(threadDir, "thread.json"), thread); err != nil {
				return err
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

// ExportAccount writes account-wide data (currently global skills).
//
// Global skills apply to every request regardless of space, so they are written
// once under account/ rather than duplicated into each space. Each skill's
// SKILL.md body is written to account/skills/<name>.md and referenced from
// account/account.json via body_file.
func (e *JSONExporter) ExportAccount(account *models.Account) error {
	if account == nil {
		return nil
	}
	dir := filepath.Join(e.OutputDir, "account")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create account directory: %w", err)
	}

	// Write skill bodies as sidecar files and record their relative paths
	// before serializing account.json so body_file is populated.
	if err := writeSkillBodiesJSON(dir, account.GlobalSkills); err != nil {
		return err
	}

	return writeJSON(filepath.Join(dir, "account.json"), account)
}

// ExportManifest writes the export manifest.
func (e *JSONExporter) ExportManifest(ctx context.Context, manifest *models.ExportManifest) error {
	// An export of nothing but the manifest has created no directory yet.
	if err := os.MkdirAll(e.OutputDir, 0755); err != nil {
		return fmt.Errorf("could not create output directory: %w", err)
	}
	return writeJSONContext(ctx, filepath.Join(e.OutputDir, "manifest.json"), manifest)
}

// threadDir returns the output directory for a thread.
func (e *JSONExporter) threadDir(thread *models.Thread) string {
	return filepath.Join(e.OutputDir, "threads", threadDirName(thread.Slug, thread.UUID))
}

// writeThreadSources keeps the optional sources sidecar in sync with thread data.
func writeThreadSources(dir string, thread *models.Thread) error {
	var sources []models.Source
	for _, entry := range thread.Entries {
		sources = append(sources, entry.Sources...)
	}

	sourcesPath := filepath.Join(dir, "sources.json")
	if len(sources) > 0 {
		return writeJSON(sourcesPath, sources)
	}
	if err := os.Remove(sourcesPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove stale sources file %s: %w", sourcesPath, err)
	}
	return nil
}

// writeSpaceSkills writes each skill's SKILL.md body into a skills/ subfolder
// of the space directory and records the relative path on the skill so that
// space.json references it. Skills without a fetched body are left as
// metadata-only. It mutates the BodyFile field of each skill in place.
func (e *JSONExporter) writeSpaceSkills(spaceDir string, skills []models.Skill) error {
	return writeSkillBodiesJSON(spaceDir, skills)
}

// writeSkillBodiesJSON writes each skill's SKILL.md body into a skills/
// subfolder of baseDir and records the relative path (skills/<name>.md) on the
// skill's BodyFile so the sibling JSON references it. Skills without a fetched
// body are left metadata-only. Filenames are collision-free across the slice.
// It mutates the BodyFile field of each skill in place.
func writeSkillBodiesJSON(baseDir string, skills []models.Skill) error {
	filenames := skillFilenames(skills)
	var skillsDir string
	for i := range skills {
		if skills[i].Body == "" {
			continue
		}
		if skillsDir == "" {
			skillsDir = filepath.Join(baseDir, "skills")
			if err := os.MkdirAll(skillsDir, 0755); err != nil {
				return fmt.Errorf("could not create skills directory: %w", err)
			}
		}
		filename := filenames[i]
		if err := writeBytes(filepath.Join(skillsDir, filename), []byte(skills[i].Body)); err != nil {
			return fmt.Errorf("could not write skill body %s: %w", filename, err)
		}
		// Use path.Join (forward slashes) so body_file is portable in JSON
		// consumed on any OS.
		skills[i].BodyFile = path.Join("skills", filename)
	}
	return nil
}

// writeJSON writes data as indented JSON to a file atomically.
//
// Data is written to a temp file in the same directory and renamed over the
// target, so a crash mid-write cannot corrupt an existing file. This matters
// for thread.json, which is rewritten on every pagination checkpoint and must
// stay a valid resume point even if a later write fails.
func writeJSON(path string, data any) error {
	return writeJSONContext(context.Background(), path, data)
}

// writeJSONContext is writeJSON that publishes nothing once ctx is done.
func writeJSONContext(ctx context.Context, path string, data any) error {
	return writeFileAtomic(ctx, path, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(data)
	})
}

// writeBytes writes data to path atomically. Like the JSON files, the file
// is private (0600): it holds the user's threads.
func writeBytes(path string, data []byte) error {
	return writeFileAtomic(context.Background(), path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// writeFileAtomic writes a temp file next to path and renames it into place,
// so readers never see a half-written file, and a symlink at path is
// replaced instead of followed. The file is created 0600 and stays so. ctx
// is checked right before the rename, so a canceled run publishes nothing.
func writeFileAtomic(ctx context.Context, path string, write func(io.Writer) error) error {
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

	if err := write(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("could not flush %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close temp file for %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("could not replace %s: %w", path, err)
	}
	tmpName = "" // rename succeeded; skip cleanup
	// Best effort: persist the rename itself. Windows cannot sync a
	// directory, and the file is already complete either way.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// DropThreadCache removes the cached thread.json written under from, now that
// the detail lives under to, so the old copy cannot be loaded. Different slugs
// can name the same directory (case, punctuation, length), and then nothing
// is removed. Other files in that directory are left as they are.
func (e *JSONExporter) DropThreadCache(uuid, from, to string) error {
	old := threadDirPath(e.OutputDir, from, uuid)
	if from == "" || old == threadDirPath(e.OutputDir, to, uuid) {
		return nil
	}
	err := os.Remove(old)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
