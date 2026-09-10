package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestPDFExportSpacesUsesCanonicalDirectories(t *testing.T) {
	dir := t.TempDir()
	exporter := NewPDFExporter(dir)
	spaces := []models.Space{
		{UUID: "aaaaaaaa1111", Name: "Recipes", ThreadUUIDs: []string{"thread-a"}},
		{UUID: "bbbbbbbb2222", Name: "recipes", ThreadUUIDs: []string{"thread-b"}},
	}
	threads := []models.Thread{
		{UUID: "thread-a", Slug: "same-slug", Title: "A"},
		{UUID: "thread-b", Slug: "same-slug", Title: "B"},
	}
	for i := range threads {
		if err := exporter.ExportThread(&threads[i]); err != nil {
			t.Fatalf("ExportThread: %v", err)
		}
	}

	if err := exporter.ExportSpaces(context.Background(), spaces, threads); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}
	dirs := spaceDirNames(spaces)
	for i, thread := range threads {
		pdfPath := filepath.Join(dir, "spaces", dirs[i], "threads", threadDirName(thread.Slug, thread.UUID), "thread.pdf")
		info, err := os.Stat(pdfPath)
		if err != nil {
			t.Fatalf("stat %s: %v", pdfPath, err)
		}
		if info.Size() == 0 {
			t.Errorf("PDF %s is empty", pdfPath)
		}
		topLevel, err := os.ReadFile(exporter.ThreadPDFPath(&threads[i]))
		if err != nil {
			t.Fatal(err)
		}
		spaceCopy, err := os.ReadFile(pdfPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(spaceCopy) != string(topLevel) {
			t.Errorf("space PDF differs from canonical thread PDF")
		}
	}
}

func TestPDFExportSpacesHonorsCancellationBeforeCopy(t *testing.T) {
	dir := t.TempDir()
	exporter := NewPDFExporter(dir)
	thread := models.Thread{UUID: "thread-a", Slug: "thread-a", Title: "A"}
	if err := exporter.ExportThread(&thread); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := exporter.ExportSpaces(ctx, []models.Space{{UUID: "space", Name: "Space", ThreadUUIDs: []string{thread.UUID}}}, []models.Thread{thread})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExportSpaces error = %v, want context.Canceled", err)
	}
}
