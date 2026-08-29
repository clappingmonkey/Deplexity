package export

import (
	"testing"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World!", "hello-world"},
		{"test/file:name", "test-file-name"},
		{"  spaces  ", "spaces"},
		{"already-safe", "already-safe"},
		{"UPPERCASE", "uppercase"},
		{"", "unnamed"},
		{"a---b---c", "a-b-c"},
		{string(make([]byte, 200)), "unnamed"}, // all zeros → all replaced → collapsed
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilenameLongInput(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := sanitizeFilename(long)
	if len(got) > 128 {
		t.Errorf("expected length <= 128, got %d", len(got))
	}
}

func TestSkillFilenamesDisambiguatesCollisions(t *testing.T) {
	// "C++ Helper" and "C# Helper" both sanitize to "c-helper"; the third is
	// unique. The two colliding entries must get distinct, ID-suffixed names.
	skills := []models.Skill{
		{ID: "aaaaaaaa1111", Name: "C++ Helper"},
		{ID: "bbbbbbbb2222", Name: "C# Helper"},
		{ID: "cccccccc3333", Name: "Recipes"},
	}

	got := skillFilenames(skills)
	if len(got) != 3 {
		t.Fatalf("got %d filenames, want 3", len(got))
	}

	// All filenames must be unique so no body overwrites another.
	seen := map[string]bool{}
	for i, name := range got {
		if seen[name] {
			t.Errorf("filename %q at index %d collides with an earlier skill", name, i)
		}
		seen[name] = true
	}

	// The colliding pair must be suffixed with a fragment of their unique IDs.
	if got[0] == got[1] {
		t.Errorf("colliding skills share filename %q", got[0])
	}
	if got[0] != "c-helper-aaaaaaaa.md" {
		t.Errorf("got[0] = %q, want c-helper-aaaaaaaa.md", got[0])
	}
	if got[1] != "c-helper-bbbbbbbb.md" {
		t.Errorf("got[1] = %q, want c-helper-bbbbbbbb.md", got[1])
	}
	// The unique skill keeps its plain base name (no suffix).
	if got[2] != "recipes.md" {
		t.Errorf("got[2] = %q, want recipes.md", got[2])
	}
}

func TestSkillFilenamesUniqueNamesAreUnsuffixed(t *testing.T) {
	skills := []models.Skill{
		{ID: "x", Name: "git-commit"},
		{ID: "y", Name: "pr-create"},
	}
	got := skillFilenames(skills)
	want := []string{"git-commit.md", "pr-create.md"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
