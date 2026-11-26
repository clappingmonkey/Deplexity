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
				UUID:   "entry-1",
				Query:  "What is Go?",
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

	path := filepath.Join(tmpDir, "threads", threadDirName(thread.Slug, thread.UUID), "thread.md")
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
	spaceDir := spaceDirNames(spaces)[0]

	// Link uses forward slashes and the collision-free filename.
	wantLink := "[git-commit](" + spaceDir + "/skills/git-commit.md)"
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
	bodyPath := filepath.Join(tmpDir, "spaces", spaceDir, "skills", "git-commit.md")
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read skill body: %v", err)
	}
	if string(body) != "# git-commit\nbody" {
		t.Errorf("skill body = %q, want fetched SKILL.md", string(body))
	}

	// The body-less skill must not produce a sidecar file.
	if _, err := os.Stat(filepath.Join(tmpDir, "spaces", spaceDir, "skills", "no-body.md")); !os.IsNotExist(err) {
		t.Errorf("unexpected sidecar for body-less skill (err=%v)", err)
	}
}

func TestMarkdownExportSpacesUsesDistinctCanonicalLinks(t *testing.T) {
	tmpDir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: tmpDir}
	spaces := []models.Space{
		{UUID: "aaaaaaaa1111", Name: "Recipes", Skills: []models.Skill{{ID: "s1", Name: "one", Body: "one"}}},
		{UUID: "bbbbbbbb2222", Name: "recipes", Skills: []models.Skill{{ID: "s2", Name: "two", Body: "two"}}},
	}
	if err := exp.ExportSpaces(context.Background(), spaces, nil); err != nil {
		t.Fatalf("ExportSpaces: %v", err)
	}
	dirs := spaceDirNames(spaces)
	overview, err := os.ReadFile(filepath.Join(tmpDir, "spaces", "spaces.md"))
	if err != nil {
		t.Fatalf("read spaces.md: %v", err)
	}
	content := string(overview)
	for i, skill := range []string{"one", "two"} {
		link := "[" + skill + "](" + dirs[i] + "/skills/" + skill + ".md)"
		if !strings.Contains(content, link) {
			t.Errorf("spaces.md missing link %q\n%s", link, content)
		}
		if _, err := os.Stat(filepath.Join(tmpDir, "spaces", dirs[i], "skills", skill+".md")); err != nil {
			t.Errorf("linked skill file missing: %v", err)
		}
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

func TestMarkdownExportAccountWritesGlobalSkills(t *testing.T) {
	tmpDir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: tmpDir}

	account := &models.Account{
		GlobalSkills: []models.Skill{
			// Body-backed skill: gets a sidecar file and a link.
			{ID: "g1", Name: "create-skill", Description: "Create skills", Body: "# create-skill\nbody"},
			// Metadata-only skill: rendered inline, no link, no file.
			{ID: "g2", Name: "no-body", Description: "No body here"},
		},
	}

	if err := exp.ExportAccount(account); err != nil {
		t.Fatalf("ExportAccount: %v", err)
	}

	overview, err := os.ReadFile(filepath.Join(tmpDir, "account", "global-skills.md"))
	if err != nil {
		t.Fatalf("read global-skills.md: %v", err)
	}
	content := string(overview)

	// Link uses forward slashes and the collision-free filename, relative to
	// global-skills.md (which lives in account/).
	wantLink := "[create-skill](skills/create-skill.md)"
	if !strings.Contains(content, wantLink) {
		t.Errorf("global-skills.md missing skill link %q\n%s", wantLink, content)
	}
	// Metadata-only skill is present but not linked.
	if !strings.Contains(content, "- no-body — No body here") {
		t.Errorf("global-skills.md missing metadata-only skill line\n%s", content)
	}
	if strings.Contains(content, "no-body.md") {
		t.Errorf("global-skills.md should not link a body-less skill\n%s", content)
	}

	// The body sidecar exists under account/skills/.
	body, err := os.ReadFile(filepath.Join(tmpDir, "account", "skills", "create-skill.md"))
	if err != nil {
		t.Fatalf("read skill body: %v", err)
	}
	if string(body) != "# create-skill\nbody" {
		t.Errorf("skill body = %q, want fetched SKILL.md", string(body))
	}

	// The body-less skill must not produce a sidecar file.
	if _, err := os.Stat(filepath.Join(tmpDir, "account", "skills", "no-body.md")); !os.IsNotExist(err) {
		t.Errorf("unexpected sidecar for body-less skill (err=%v)", err)
	}
}

func TestMarkdownExportAccountEmptyAndNil(t *testing.T) {
	tmpDir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: tmpDir}

	// Empty global-skills set still writes the overview with a placeholder.
	if err := exp.ExportAccount(&models.Account{}); err != nil {
		t.Fatalf("ExportAccount(empty): %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, "account", "global-skills.md"))
	if err != nil {
		t.Fatalf("read global-skills.md: %v", err)
	}
	if !strings.Contains(string(content), "_No global skills._") {
		t.Errorf("empty account missing placeholder\n%s", content)
	}

	// Nil account is a no-op: it must not create the account dir.
	tmpDir2 := t.TempDir()
	exp2 := &MarkdownExporter{OutputDir: tmpDir2}
	if err := exp2.ExportAccount(nil); err != nil {
		t.Fatalf("ExportAccount(nil): %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir2, "account")); !os.IsNotExist(err) {
		t.Errorf("nil account created an account dir (err=%v)", err)
	}
}

func TestMarkdownLink(t *testing.T) {
	tests := []struct{ text, target, want string }{
		{"Plain", "https://example.com/a", "[Plain](https://example.com/a)"},
		{"a [b] c", "https://example.com", `[a \[b\] c](https://example.com)`},
		{"x](javascript:alert(1)) [y", "https://example.com", `[x\](javascript:alert(1)) \[y](https://example.com)`},
		{"Wiki", "https://en.wikipedia.org/wiki/Go_(language)", "[Wiki](<https://en.wikipedia.org/wiki/Go_(language)>)"},
		{"Spaced", "https://example.com/a b>c", "[Spaced](https://example.com/a%20b%3Ec)"},
		{"Query", "https://example.com/?a=1&b=2", "[Query](https://example.com/?a=1&amp;b=2)"},
		{"Line\nbreak", "skills/x.md", "[Line break](skills/x.md)"},
		{"Mail", "mailto:a@example.com", "[Mail](mailto:a@example.com)"},
		{"Caps", "HTTPS://example.com/a", "[Caps](HTTPS://example.com/a)"},
		// Targets that are not http(s) or mailto, as Markdown would read them.
		{"Evil", "javascript:alert(1)", "Evil"},
		{"Upper", "JavaScript:alert(1)", "Upper"},
		{"Data", "data:text/html,<script>", "Data"},
		{"Tab", "java\tscript:alert(1)", "Tab"},
		{"Space", " javascript:alert(1)", "Space"},
		{"Empty", "", "Empty"},
		// An entity or a backslash escape must not turn into a ':' later.
		{"Entity", "javascript&#58;alert(1)", "[Entity](<javascript&amp;#58;alert(1)>)"},
		{"Escape", `javascript\:alert(1)`, "Escape"},
		{"Backslash", `https://example.com/a\b`, "[Backslash](https://example.com/a%5Cb)"},
		// HTML and code spans in the text stay text.
		{"<img src=x onerror=alert(1)>", "https://example.com", `[\<img src=x onerror=alert(1)\>](https://example.com)`},
		{"a `b", "https://example.com", "[a \\`b](https://example.com)"},
	}
	for _, tt := range tests {
		if got := markdownLink(tt.text, tt.target); got != tt.want {
			t.Errorf("markdownLink(%q, %q) = %q, want %q", tt.text, tt.target, got, tt.want)
		}
	}
}

func TestMarkdownText(t *testing.T) {
	got := markdownText("<b>x</b> & *y* [z](u) `c` a_b\nnext")
	want := "\\<b\\>x\\</b\\> \\& \\*y\\* \\[z\\](u) \\`c\\` a\\_b next"
	if got != want {
		t.Errorf("markdownText = %q, want %q", got, want)
	}
}

func TestMarkdownTimestampsAreUTC(t *testing.T) {
	dir := t.TempDir()
	exp := &MarkdownExporter{OutputDir: dir}
	created := time.Date(2026, 3, 4, 10, 0, 0, 0, time.FixedZone("EET", 2*60*60))
	thread := &models.Thread{UUID: "u", Slug: "s", Title: "T", CreatedAt: created, UpdatedAt: created}
	if err := exp.ExportThread(thread); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "threads", "*", "*.md"))
	if len(matches) != 1 {
		t.Fatalf("markdown files = %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "2026-03-04 08:00:00 UTC") {
		t.Errorf("markdown does not show the UTC time:\n%s", data)
	}
}
