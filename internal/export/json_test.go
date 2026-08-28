package export

import (
	"os"
	"path/filepath"
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
