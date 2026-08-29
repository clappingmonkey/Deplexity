package export

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestLoadCompleteThread(t *testing.T) {
	exporter := &JSONExporter{OutputDir: t.TempDir()}
	thread := &models.Thread{UUID: "thread-1", Slug: "thread-1"}
	if err := exporter.ExportThread(thread); err != nil {
		t.Fatalf("ExportThread: %v", err)
	}

	if _, err := exporter.LoadCompleteThread(thread.UUID); err == nil {
		t.Fatal("LoadCompleteThread succeeded for an incomplete thread")
	}

	thread.Complete = true
	if err := exporter.ExportThread(thread); err != nil {
		t.Fatalf("ExportThread: %v", err)
	}

	loaded, err := exporter.LoadCompleteThread(thread.UUID)
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if !loaded.Complete {
		t.Fatal("loaded thread is not complete")
	}
}

func TestLoadCompleteThreadPreservesMetadata(t *testing.T) {
	exporter := &JSONExporter{OutputDir: t.TempDir()}
	updatedAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	thread := &models.Thread{UUID: "thread-1", Slug: "thread-1", UpdatedAt: updatedAt, Complete: true}
	if err := exporter.ExportThread(thread); err != nil {
		t.Fatalf("ExportThread: %v", err)
	}

	loaded, err := exporter.LoadCompleteThread(thread.UUID)
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if !loaded.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", loaded.UpdatedAt, updatedAt)
	}
}

func TestExportSpacesWritesInstructionsAndSkills(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}

	space := models.Space{
		UUID:         "e79179d1",
		Name:         "Recipes",
		Slug:         "recipes-55F50RUIQUK_fqfJUieN1w",
		Instructions: "Test for the deplexity tool",
		SuggestedQueries: []string{"How do I sear steak?"},
		Skills: []models.Skill{
			{
				ID:    "skill-collection",
				Name:  "git-commit",
				Scope: "collection",
				Body:  "---\nname: git-commit\n---\nCommit helper body",
			},
			{
				ID:    "skill-nobody",
				Name:  "no-body-skill",
				Scope: "collection",
				// No Body — metadata only, must not produce a file.
			},
		},
	}

	if err := exporter.ExportSpaces(context.Background(), []models.Space{space}, nil); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}

	// The exporter sanitizes (and lowercases) the space name for the folder,
	// so derive the expected path the same way rather than hardcoding "Recipes"
	// (which passes on case-insensitive macOS but fails on case-sensitive Linux).
	spaceDir := filepath.Join(dir, "spaces", sanitizeFilename(space.Name))

	// The skill body file must exist with the fetched content.
	bodyPath := filepath.Join(spaceDir, "skills", "git-commit.md")
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read skill body: %v", err)
	}
	if !strings.Contains(string(body), "Commit helper body") {
		t.Errorf("skill body = %q, want the fetched SKILL.md", string(body))
	}

	// The body-less skill must not create a file.
	if _, err := os.Stat(filepath.Join(spaceDir, "skills", "no-body-skill.md")); !os.IsNotExist(err) {
		t.Errorf("body-less skill produced a file, want none (err=%v)", err)
	}

	// space.json must carry instructions and skills metadata with body_file set.
	var written models.Space
	raw, err := os.ReadFile(filepath.Join(spaceDir, "space.json"))
	if err != nil {
		t.Fatalf("read space.json: %v", err)
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("unmarshal space.json: %v", err)
	}
	if written.Instructions != "Test for the deplexity tool" {
		t.Errorf("Instructions = %q, want sentinel", written.Instructions)
	}
	if len(written.Skills) != 2 {
		t.Fatalf("got %d skills in space.json, want 2", len(written.Skills))
	}
	// BodyFile always uses forward slashes so it is portable inside JSON.
	if written.Skills[0].BodyFile != "skills/git-commit.md" {
		t.Errorf("BodyFile = %q, want skills/git-commit.md", written.Skills[0].BodyFile)
	}
	if written.Skills[1].BodyFile != "" {
		t.Errorf("body-less skill BodyFile = %q, want empty", written.Skills[1].BodyFile)
	}
	// Body must never be serialized into JSON.
	if strings.Contains(string(raw), "Commit helper body") {
		t.Error("space.json leaked the skill body; Body should be json:\"-\"")
	}
}

func TestWriteJSONFailedWriteLeavesExistingFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thread.json")

	if err := writeJSON(path, map[string]string{"state": "good"}); err != nil {
		t.Fatalf("writeJSON (initial): %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read initial file: %v", err)
	}

	// A channel cannot be JSON-encoded, so Encode fails after the temp file
	// is created but before the rename. The original file must survive.
	if err := writeJSON(path, make(chan int)); err == nil {
		t.Fatal("writeJSON succeeded encoding an unserializable value")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("original file missing after failed write: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("original file changed after failed write:\n before=%s\n after=%s", before, after)
	}

	// No temp files should be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "thread.json" {
			t.Errorf("leftover file after failed write: %s", e.Name())
		}
	}
}
