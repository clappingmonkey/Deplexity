package export

import (
	"context"
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

func TestMarkdownExportSpacesWithSkills(t *testing.T) {
	tmpDir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: tmpDir}

	spaces := []models.Space{
		{
			UUID:         "space-1",
			Name:         "Recipes",
			Description:  "A cooking space",
			Instructions: "Test for the deplexity tool",
			Skills: []models.Skill{
				// Body-backed skill: gets a sidecar file and a link.
				{ID: "s1", Name: "git-commit", Description: "Commit helper", Body: "# git-commit\nbody"},
				// Metadata-only skill: rendered inline, no link, no file.
				{ID: "s2", Name: "no-body", Description: "No body here"},
			},
		},
	}

	if err := exp.ExportSpaces(context.Background(), spaces, nil); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}

	// spaces.md content.
	overview, err := os.ReadFile(filepath.Join(tmpDir, "spaces", "spaces.md"))
	if err != nil {
		t.Fatalf("read spaces.md: %v", err)
	}
	content := string(overview)

	// Link uses forward slashes and the collision-free filename.
	wantLink := "[git-commit](recipes/skills/git-commit.md)"
	if !strings.Contains(content, wantLink) {
		t.Errorf("spaces.md missing skill link %q\n%s", wantLink, content)
	}
	// Metadata-only skill is present but not linked.
	if !strings.Contains(content, "- no-body — No body here") {
		t.Errorf("spaces.md missing metadata-only skill line\n%s", content)
	}
	if strings.Contains(content, "no-body.md") {
		t.Errorf("spaces.md should not link a body-less skill\n%s", content)
	}
	if !strings.Contains(content, "Test for the deplexity tool") {
		t.Errorf("spaces.md missing instructions sentinel\n%s", content)
	}

	// The body sidecar file exists with the skill body.
	bodyPath := filepath.Join(tmpDir, "spaces", "recipes", "skills", "git-commit.md")
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read skill body: %v", err)
	}
	if string(body) != "# git-commit\nbody" {
		t.Errorf("skill body = %q, want fetched SKILL.md", string(body))
	}

	// The body-less skill must not produce a sidecar file.
	if _, err := os.Stat(filepath.Join(tmpDir, "spaces", "recipes", "skills", "no-body.md")); !os.IsNotExist(err) {
		t.Errorf("unexpected sidecar for body-less skill (err=%v)", err)
	}
}

func TestBlockquoteMultiline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single line", "hello", "> hello\n"},
		{"two lines", "line one\nline two", "> line one\n> line two\n"},
		{"blank line between", "a\n\nb", "> a\n>\n> b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockquote(tc.in); got != tc.want {
				t.Errorf("blockquote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownExportSpacesMultilineInstructions(t *testing.T) {
	tmpDir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: tmpDir}

	spaces := []models.Space{{
		UUID:         "space-1",
		Name:         "Recipes",
		Instructions: "First line.\nSecond line.",
	}}
	if err := exp.ExportSpaces(context.Background(), spaces, nil); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}

	overview, err := os.ReadFile(filepath.Join(tmpDir, "spaces", "spaces.md"))
	if err != nil {
		t.Fatalf("read spaces.md: %v", err)
	}
	content := string(overview)
	// Every instruction line must be individually blockquoted.
	if !strings.Contains(content, "> First line.\n> Second line.\n") {
		t.Errorf("multiline instructions not blockquoted per line\n%s", content)
	}
}
