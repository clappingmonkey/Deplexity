package export

import (
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
