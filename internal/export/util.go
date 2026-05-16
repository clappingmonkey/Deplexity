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
