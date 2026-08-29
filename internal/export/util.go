package export

import (
	"regexp"
	"strings"

	"github.com/clappingmonkey/deplexity/internal/models"
)

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeFilename replaces characters unsafe for file/directory names.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")

	// Collapse multiple dashes
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}

	if name == "" {
		name = "unnamed"
	}

	// Limit length
	if len(name) > 128 {
		name = name[:128]
	}

	return strings.ToLower(name)
}

// threadSlug returns the slug (or UUID fallback) for a thread's directory name.
func threadSlug(t *models.Thread) string {
	if t.Slug != "" {
		return t.Slug
	}
	return t.UUID
}

// skillFilenames returns a collision-free ".md" filename for each skill, keyed
// by slice index. sanitizeFilename can map distinct skill names to the same
// base (e.g. "C++ Helper" and "C# Helper" both collapse to "c-helper"), which
// would otherwise cause one skill's body to silently overwrite another. When a
// base name repeats, a short suffix derived from the unique skill ID is
// appended. Both the JSON and Markdown exporters call this so they agree on the
// on-disk filename and the links pointing at it.
func skillFilenames(skills []models.Skill) []string {
	names := make([]string, len(skills))

	// Count how many skills map to each sanitized base name.
	counts := make(map[string]int, len(skills))
	for i := range skills {
		counts[sanitizeFilename(skills[i].Name)]++
	}

	used := make(map[string]bool, len(skills))
	for i := range skills {
		base := sanitizeFilename(skills[i].Name)
		name := base
		if counts[base] > 1 {
			name = base + "-" + shortID(skills[i].ID)
		}
		// Final guard against any residual collision (e.g. empty IDs).
		for used[name] {
			name += "-x"
		}
		used[name] = true
		names[i] = name + ".md"
	}
	return names
}

// shortID returns a short, filesystem-safe fragment of a skill ID for
// disambiguating filenames. Falls back to a sanitized form when the ID is
// short or non-hex.
func shortID(id string) string {
	s := sanitizeFilename(id)
	if s == "unnamed" {
		return "id"
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
