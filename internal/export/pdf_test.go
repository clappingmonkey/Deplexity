package export

import (
	"context"
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
		{UUID: "thread-a", Slug: "thread-a", Title: "A"},
		{UUID: "thread-b", Slug: "thread-b", Title: "B"},
	}

	if err := exporter.ExportSpaces(context.Background(), spaces, threads); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}
	dirs := spaceDirNames(spaces)
	for i, thread := range threads {
		pdfPath := filepath.Join(dir, "spaces", dirs[i], "threads", thread.Slug, "thread.pdf")
		info, err := os.Stat(pdfPath)
		if err != nil {
			t.Fatalf("stat %s: %v", pdfPath, err)
		}
		if info.Size() == 0 {
			t.Errorf("PDF %s is empty", pdfPath)
		}
	}
}
