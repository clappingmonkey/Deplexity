package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestMarkdownExportThread(t *testing.T) {
	tmpDir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: tmpDir}

	thread := &models.Thread{
		UUID:      "uuid-1",
		Slug:      "test-thread",
		Title:     "Test Thread Title",
		CreatedAt: time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2024, 6, 16, 12, 0, 0, 0, time.UTC),
		Entries: []models.Entry{
			{
				UUID:  "entry-1",
				Query: "What is Go?",
				Answer: "Go is a programming language.",
				Sources: []models.Source{
					{Title: "Go Website", URL: "https://go.dev"},
				},
				Model: "gpt-4",
			},
		},
	}

	if err := exp.ExportThread(thread); err != nil {
		t.Fatalf("ExportThread: %v", err)
	}

	path := filepath.Join(tmpDir, "threads", "test-thread", "thread.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	content := string(data)

	checks := []string{
		"# Test Thread Title",
		"## Q: What is Go?",
		"Go is a programming language.",
		"[Go Website](https://go.dev)",
		"*Model: gpt-4*",
	}

	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("markdown missing %q", check)
		}
	}
}
