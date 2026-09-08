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

	ref := models.ThreadRef{UUID: thread.UUID, Slug: thread.Slug}
	if _, err := exporter.LoadCompleteThread(ref); err == nil {
		t.Fatal("LoadCompleteThread succeeded for an incomplete thread")
	}

	thread.Complete = true
	if err := exporter.ExportThread(thread); err != nil {
		t.Fatalf("ExportThread: %v", err)
	}

	loaded, err := exporter.LoadCompleteThread(ref)
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

	loaded, err := exporter.LoadCompleteThread(models.ThreadRef{UUID: thread.UUID, Slug: thread.Slug})
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if !loaded.UpdatedAt.Equal(updatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", loaded.UpdatedAt, updatedAt)
	}
}

func TestLoadThreadFallsBackToLegacyUUIDDirectory(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	legacyDir := filepath.Join(dir, "threads", "thread-1")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("create legacy directory: %v", err)
	}
	legacy := models.Thread{UUID: "thread-1", Slug: "thread-1", Complete: true}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy thread: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "thread.json"), data, 0600); err != nil {
		t.Fatalf("write legacy thread: %v", err)
	}

	ref := models.ThreadRef{UUID: "thread-1", Slug: "human-readable-slug"}
	loaded, err := exporter.LoadCompleteThread(ref)
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if loaded.UUID != ref.UUID {
		t.Errorf("loaded UUID = %q, want %q", loaded.UUID, ref.UUID)
	}
	if exporter.HasCanonicalThread(ref) {
		t.Fatal("legacy load unexpectedly created the canonical path")
	}
}

func TestLoadThreadUsesFirstMatchingUUIDCandidate(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	ref := models.ThreadRef{UUID: "thread-1", Slug: "current", PreviousSlug: "previous"}

	writeCandidate := func(slug, uuid, title string) {
		t.Helper()
		candidateDir := filepath.Join(dir, "threads", threadDirName(slug, ref.UUID))
		if err := os.MkdirAll(candidateDir, 0755); err != nil {
			t.Fatalf("create candidate directory: %v", err)
		}
		data, err := json.Marshal(models.Thread{UUID: uuid, Slug: slug, Title: title, Complete: true})
		if err != nil {
			t.Fatalf("marshal candidate: %v", err)
		}
		if err := os.WriteFile(filepath.Join(candidateDir, "thread.json"), data, 0600); err != nil {
			t.Fatalf("write candidate: %v", err)
		}
	}

	writeCandidate("current", "wrong-thread", "wrong current")
	writeCandidate("previous", ref.UUID, "matching previous")
	loaded, err := exporter.LoadCompleteThread(ref)
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if loaded.Title != "matching previous" {
		t.Errorf("loaded title = %q, want matching previous candidate", loaded.Title)
	}

	writeCandidate("current", ref.UUID, "matching current")
	loaded, err = exporter.LoadCompleteThread(ref)
	if err != nil {
		t.Fatalf("LoadCompleteThread with current candidate: %v", err)
	}
	if loaded.Title != "matching current" {
		t.Errorf("loaded title = %q, want current candidate precedence", loaded.Title)
	}
}

func TestLoadThreadFindsCanonicalCacheWrittenBeforeSlugWasKnown(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	thread := &models.Thread{UUID: "thread-1", Complete: true}
	if err := exporter.ExportThread(thread); err != nil {
		t.Fatalf("ExportThread: %v", err)
	}

	ref := models.ThreadRef{UUID: thread.UUID, Slug: "newly-listed-slug"}
	loaded, err := exporter.LoadCompleteThread(ref)
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if loaded.UUID != ref.UUID {
		t.Errorf("loaded UUID = %q, want %q", loaded.UUID, ref.UUID)
	}
}

func TestLoadThreadRejectsWrongUUIDInEveryCandidate(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	ref := models.ThreadRef{UUID: "thread-1", Slug: "current", PreviousSlug: "previous"}
	paths := []string{
		filepath.Join(dir, "threads", threadDirName(ref.Slug, ref.UUID)),
		filepath.Join(dir, "threads", threadDirName(ref.PreviousSlug, ref.UUID)),
		filepath.Join(dir, "threads", sanitizeFilename(ref.UUID)),
	}
	for _, candidateDir := range paths {
		if err := os.MkdirAll(candidateDir, 0755); err != nil {
			t.Fatalf("create candidate directory: %v", err)
		}
		data, err := json.Marshal(models.Thread{UUID: "wrong-thread", Complete: true})
		if err != nil {
			t.Fatalf("marshal candidate: %v", err)
		}
		if err := os.WriteFile(filepath.Join(candidateDir, "thread.json"), data, 0600); err != nil {
			t.Fatalf("write candidate: %v", err)
		}
	}

	if _, err := exporter.LoadCompleteThread(ref); err == nil || !strings.Contains(err.Error(), "want \"thread-1\"") {
		t.Fatalf("error = %v, want UUID mismatch", err)
	}
}

func TestExportThreadMarksCacheIncompleteUntilSidecarsSucceed(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	thread := &models.Thread{
		UUID:     "thread-1",
		Slug:     "thread-1",
		Complete: true,
		Entries:  []models.Entry{{Sources: []models.Source{{URL: "https://example.com"}}}},
	}
	threadDir := exporter.threadDir(thread)
	if err := os.MkdirAll(filepath.Join(threadDir, "sources.json"), 0755); err != nil {
		t.Fatalf("create blocking sources directory: %v", err)
	}

	if err := exporter.ExportThread(thread); err == nil {
		t.Fatal("ExportThread succeeded with blocked sources.json")
	}
	ref := models.ThreadRef{UUID: thread.UUID, Slug: thread.Slug}
	if _, err := exporter.LoadCompleteThread(ref); err == nil {
		t.Fatal("LoadCompleteThread accepted cache after sidecar write failure")
	}
	partial, err := exporter.LoadThread(ref)
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if partial.Complete {
		t.Fatal("thread.json remains complete after sidecar write failure")
	}
}

func TestExportSpacesWritesInstructionsAndSkills(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}

	space := models.Space{
		UUID:             "e79179d1",
		Name:             "Recipes",
		Slug:             "recipes-55F50RUIQUK_fqfJUieN1w",
		Instructions:     "Test for the deplexity tool",
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

	// Derive the canonical, identity-suffixed directory through the same shared
	// helper used by every exporter.
	spaceDir := filepath.Join(dir, "spaces", spaceDirNames([]models.Space{space})[0])

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

func TestExportSpacesSeparatesCollidingNames(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	spaces := []models.Space{
		{UUID: "aaaaaaaa1111", Name: "Recipes", ThreadUUIDs: []string{"thread-a"}},
		{UUID: "bbbbbbbb2222", Name: "recipes", ThreadUUIDs: []string{"thread-b"}},
	}
	threads := []models.Thread{
		{UUID: "thread-a", Slug: "thread-a", Complete: true},
		{UUID: "thread-b", Slug: "thread-b", Complete: true},
	}

	if err := exporter.ExportSpaces(context.Background(), spaces, threads); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}
	dirs := spaceDirNames(spaces)
	if dirs[0] == dirs[1] {
		t.Fatalf("colliding spaces share directory %q", dirs[0])
	}
	for i, space := range spaces {
		spaceDir := filepath.Join(dir, "spaces", dirs[i])
		raw, err := os.ReadFile(filepath.Join(spaceDir, "space.json"))
		if err != nil {
			t.Fatalf("read %s space.json: %v", space.UUID, err)
		}
		var written models.Space
		if err := json.Unmarshal(raw, &written); err != nil {
			t.Fatalf("unmarshal %s space.json: %v", space.UUID, err)
		}
		if written.UUID != space.UUID {
			t.Errorf("space dir %q contains UUID %q, want %q", dirs[i], written.UUID, space.UUID)
		}
		ownThread := filepath.Join(spaceDir, "threads", threadDirName(threads[i].Slug, threads[i].UUID), "thread.json")
		if _, err := os.Stat(ownThread); err != nil {
			t.Errorf("space %s missing own thread: %v", space.UUID, err)
		}
		otherThread := filepath.Join(spaceDir, "threads", threadDirName(threads[1-i].Slug, threads[1-i].UUID), "thread.json")
		if _, err := os.Stat(otherThread); !os.IsNotExist(err) {
			t.Errorf("space %s contains other space thread (err=%v)", space.UUID, err)
		}
	}
}

func TestExportSpacesSeparatesCollidingThreadSlugs(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	space := models.Space{UUID: "space-1", Name: "Space", ThreadUUIDs: []string{"thread-a", "thread-b"}}
	threads := []models.Thread{
		{UUID: "thread-a", Slug: "C++", Complete: true},
		{UUID: "thread-b", Slug: "C#", Complete: true},
	}

	if err := exporter.ExportSpaces(context.Background(), []models.Space{space}, threads); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}
	spaceDir := filepath.Join(dir, "spaces", spaceDirNames([]models.Space{space})[0], "threads")
	for _, thread := range threads {
		path := filepath.Join(spaceDir, threadDirName(thread.Slug, thread.UUID), "thread.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var written models.Thread
		if err := json.Unmarshal(data, &written); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		if written.UUID != thread.UUID {
			t.Errorf("%s contains UUID %q, want %q", path, written.UUID, thread.UUID)
		}
	}
}

func TestExportSpacesLeavesLegacyDirectoryUntouched(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "spaces", "recipes")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}
	sentinel := filepath.Join(legacyDir, "legacy.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatalf("write legacy sentinel: %v", err)
	}
	space := models.Space{UUID: "e79179d1", Name: "Recipes"}
	exporter := &JSONExporter{OutputDir: dir}
	if err := exporter.ExportSpaces(context.Background(), []models.Space{space}, nil); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "keep" {
		t.Fatalf("legacy directory changed: body=%q err=%v", body, err)
	}
	newDir := filepath.Join(dir, "spaces", spaceDirNames([]models.Space{space})[0])
	if _, err := os.Stat(filepath.Join(newDir, "space.json")); err != nil {
		t.Fatalf("canonical space directory missing: %v", err)
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

func TestExportAccountWritesGlobalSkills(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}

	account := &models.Account{
		GlobalSkills: []models.Skill{
			{
				ID:    "g1",
				Name:  "create-skill",
				Scope: "global",
				Body:  "---\nname: create-skill\n---\nGlobal body",
			},
			{
				ID:    "g2",
				Name:  "no-body-skill",
				Scope: "global",
				// No Body — metadata only, must not produce a file.
			},
		},
	}

	if err := exporter.ExportAccount(account); err != nil {
		t.Fatalf("ExportAccount: %v", err)
	}

	accountDir := filepath.Join(dir, "account")

	// The skill body file must exist under account/skills/.
	body, err := os.ReadFile(filepath.Join(accountDir, "skills", "create-skill.md"))
	if err != nil {
		t.Fatalf("read skill body: %v", err)
	}
	if !strings.Contains(string(body), "Global body") {
		t.Errorf("skill body = %q, want fetched SKILL.md", string(body))
	}

	// The body-less skill must not create a file.
	if _, err := os.Stat(filepath.Join(accountDir, "skills", "no-body-skill.md")); !os.IsNotExist(err) {
		t.Errorf("body-less skill produced a file, want none (err=%v)", err)
	}

	// account.json must carry skills metadata with body_file set on the one
	// that has a body, and must not leak the body content.
	raw, err := os.ReadFile(filepath.Join(accountDir, "account.json"))
	if err != nil {
		t.Fatalf("read account.json: %v", err)
	}
	var written models.Account
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("unmarshal account.json: %v", err)
	}
	if len(written.GlobalSkills) != 2 {
		t.Fatalf("got %d global skills, want 2", len(written.GlobalSkills))
	}
	if written.GlobalSkills[0].BodyFile != "skills/create-skill.md" {
		t.Errorf("BodyFile = %q, want skills/create-skill.md", written.GlobalSkills[0].BodyFile)
	}
	if written.GlobalSkills[1].BodyFile != "" {
		t.Errorf("body-less skill BodyFile = %q, want empty", written.GlobalSkills[1].BodyFile)
	}
	if strings.Contains(string(raw), "Global body") {
		t.Error("account.json leaked the skill body; Body should be json:\"-\"")
	}
}

func TestExportAccountNilIsNoop(t *testing.T) {
	dir := t.TempDir()
	exporter := &JSONExporter{OutputDir: dir}
	if err := exporter.ExportAccount(nil); err != nil {
		t.Fatalf("ExportAccount(nil): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "account")); !os.IsNotExist(err) {
		t.Errorf("nil account created an account dir (err=%v)", err)
	}
}
