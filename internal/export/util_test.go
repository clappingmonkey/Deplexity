package export

import (
	"strings"
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

func TestSpaceDirNamesUseStableIdentitySuffixes(t *testing.T) {
	spaces := []models.Space{
		{UUID: "aaaaaaaa1111", Name: "Recipes"},
		{UUID: "bbbbbbbb2222", Name: "recipes"},
		{UUID: "cccccccc3333", Name: "C++ Helper"},
		{UUID: "dddddddd4444", Name: "C# Helper"},
	}
	got := spaceDirNames(spaces)
	for i := range got {
		if len(got[i]) > maxFilenameLength {
			t.Errorf("got[%d] length = %d, want <= %d", i, len(got[i]), maxFilenameLength)
		}
		if !strings.HasPrefix(got[i], []string{"recipes-aaaaaaaa-", "recipes-bbbbbbbb-", "c-helper-cccccccc-", "c-helper-dddddddd-"}[i]) {
			t.Errorf("got[%d] = %q, want readable identity prefix", i, got[i])
		}
	}
	if got[0] == got[1] || got[2] == got[3] {
		t.Fatalf("colliding display names were not disambiguated: %v", got)
	}
}

func TestSpaceDirNamesKeepSuffixAfterTruncation(t *testing.T) {
	prefix := strings.Repeat("a", 140)
	spaces := []models.Space{
		{UUID: "aaaaaaaa1111", Name: prefix + "one"},
		{UUID: "bbbbbbbb2222", Name: prefix + "two"},
	}
	got := spaceDirNames(spaces)
	if got[0] == got[1] {
		t.Fatalf("truncated space names collided: %q", got[0])
	}
	if len(got[0]) > maxFilenameLength || !strings.Contains(got[0], "-aaaaaaaa-") {
		t.Errorf("got[0] = %q, want bounded name with stable suffix", got[0])
	}
	if len(got[1]) > maxFilenameLength || !strings.Contains(got[1], "-bbbbbbbb-") {
		t.Errorf("got[1] = %q, want bounded name with stable suffix", got[1])
	}
}

func TestSpaceDirNamesHandleUnsafeAndDuplicateIdentities(t *testing.T) {
	spaces := []models.Space{
		{UUID: "same-id", Name: "."},
		{UUID: "same-id", Name: ".."},
		{Slug: "slug-value", Name: ""},
		{Name: "   "},
	}
	want := []string{
		"space-same-id-058be655fae2a4c37a21d2c091ba7661507619c9b348e090c3eb715bf552f1e5",
		"space-same-id-058be655fae2a4c37a21d2c091ba7661507619c9b348e090c3eb715bf552f1e5-2",
		"unnamed-slug-val-bb7c3ff182a5fb1f1da473a0be71d1483cef53d7f8d120b5622208b8352200da",
		"unnamed-id",
	}
	got := spaceDirNames(spaces)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
		if got[i] == "." || got[i] == ".." {
			t.Errorf("got unsafe path component %q", got[i])
		}
	}
}

func TestSpaceDirNamesAreStableForIdentitiesWithSameTruncatedHash(t *testing.T) {
	first := models.Space{UUID: "aaaaaaaa-cd9a-5224-e37f-87674679dc48", Name: "Recipes"}
	second := models.Space{UUID: "aaaaaaaa-98fa-e548-7436-4992599ad117", Name: "Recipes"}
	forward := spaceDirNames([]models.Space{first, second})
	reverse := spaceDirNames([]models.Space{second, first})

	if forward[0] != reverse[1] || forward[1] != reverse[0] {
		t.Fatalf("full-hash identity mapping changed after reorder: forward=%v reverse=%v", forward, reverse)
	}
	if forward[0] == forward[1] {
		t.Fatalf("distinct identities with colliding 32-bit hash prefixes share %q", forward[0])
	}
}

func TestSpaceDirNamesHashNonEmptyUnsafeIdentities(t *testing.T) {
	first := models.Space{Slug: "???", Name: "Same"}
	second := models.Space{Slug: "!!!", Name: "Same"}
	forward := spaceDirNames([]models.Space{first, second})
	reverse := spaceDirNames([]models.Space{second, first})

	if forward[0] != reverse[1] || forward[1] != reverse[0] {
		t.Fatalf("unsafe identity mapping changed after reorder: forward=%v reverse=%v", forward, reverse)
	}
	if forward[0] == forward[1] {
		t.Fatalf("distinct unsafe identities share %q", forward[0])
	}
}

func TestSpaceDirNamesAreStableForShortSanitizationCollisions(t *testing.T) {
	first := models.Space{Slug: "a!", Name: "Same"}
	second := models.Space{Slug: "a@", Name: "Same"}
	forward := spaceDirNames([]models.Space{first, second})
	reverse := spaceDirNames([]models.Space{second, first})

	if forward[0] != reverse[1] {
		t.Errorf("first space moved from %q to %q after reorder", forward[0], reverse[1])
	}
	if forward[1] != reverse[0] {
		t.Errorf("second space moved from %q to %q after reorder", forward[1], reverse[0])
	}
	if forward[0] == forward[1] {
		t.Fatalf("short malformed identities collided: %q", forward[0])
	}
}

func TestSpaceDirNamesAreStableWhenIdentityPrefixesCollide(t *testing.T) {
	first := models.Space{UUID: "aaaaaaaa1111", Name: "Recipes"}
	second := models.Space{UUID: "aaaaaaaa2222", Name: "Recipes"}
	forward := spaceDirNames([]models.Space{first, second})
	reverse := spaceDirNames([]models.Space{second, first})

	if forward[0] != reverse[1] {
		t.Errorf("first space moved from %q to %q after reorder", forward[0], reverse[1])
	}
	if forward[1] != reverse[0] {
		t.Errorf("second space moved from %q to %q after reorder", forward[1], reverse[0])
	}
	if forward[0] == forward[1] {
		t.Fatalf("distinct identities with shared prefix collided: %q", forward[0])
	}
}

func TestThreadDirNameUsesSlugAndFullUUIDIdentity(t *testing.T) {
	tests := []struct {
		name string
		slug string
		uuid string
	}{
		{name: "normal", slug: "Readable Thread", uuid: "aaaaaaaa1111"},
		{name: "empty slug", uuid: "bbbbbbbb2222"},
		{name: "unsafe slug", slug: "..", uuid: "cccccccc3333"},
		{name: "long slug", slug: strings.Repeat("a", 200), uuid: "dddddddd4444"},
		{name: "trimmed empty slug", slug: strings.Repeat(".-", 100), uuid: "eeeeeeee5555"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := threadDirName(tt.slug, tt.uuid)
			if len(got) > maxFilenameLength {
				t.Fatalf("length = %d, want <= %d", len(got), maxFilenameLength)
			}
			if got == "." || got == ".." || !strings.Contains(got, spaceIdentitySuffix(tt.uuid)) {
				t.Errorf("threadDirName(%q, %q) = %q, want safe UUID identity suffix", tt.slug, tt.uuid, got)
			}
		})
	}

	first := threadDirName("C++", "aaaaaaaa-cd9a-5224-e37f-87674679dc48")
	second := threadDirName("C#", "aaaaaaaa-98fa-e548-7436-4992599ad117")
	if first == second {
		t.Fatalf("normalized slug and shared hash-prefix collision produced %q", first)
	}
}
