package export

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/clappingmonkey/deplexity/internal/models"
)

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

const maxFilenameLength = 128

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
	if len(name) > maxFilenameLength {
		name = name[:maxFilenameLength]
	}

	return strings.ToLower(name)
}

// spaceDirNames returns a collision-free directory name for each space, keyed
// by slice index. The readable name is always suffixed with a stable fragment
// of the space UUID (or slug fallback), so case folding, punctuation removal,
// and truncation cannot make distinct spaces share a directory. All exporters
// use this helper so filesystem paths and Markdown links stay aligned.
func spaceDirNames(spaces []models.Space) []string {
	names := make([]string, len(spaces))
	used := make(map[string]bool, len(spaces))

	for i := range spaces {
		identity := spaces[i].UUID
		if identity == "" {
			identity = spaces[i].Slug
		}
		suffix := spaceIdentitySuffix(identity)
		base := sanitizeFilename(spaces[i].Name)
		if base == "" || base == "." || base == ".." {
			base = "space"
		}

		discriminator := ""
		for n := 1; ; n++ {
			maxBaseLength := maxFilenameLength - len(suffix) - len(discriminator) - 1
			trimmedBase := base
			if len(trimmedBase) > maxBaseLength {
				trimmedBase = strings.TrimRight(trimmedBase[:maxBaseLength], ".-")
			}
			name := trimmedBase + "-" + suffix + discriminator
			if !used[name] {
				used[name] = true
				names[i] = name
				break
			}
			discriminator = fmt.Sprintf("-%d", n+1)
		}
	}

	return names
}

// spaceIdentitySuffix combines a readable identity prefix with the full hash of
// the raw identity, so distinct identities remain stable regardless of ordering.
func spaceIdentitySuffix(identity string) string {
	if identity == "" {
		return "id"
	}
	sum := sha256.Sum256([]byte(identity))
	safe := strings.TrimRight(sanitizeFilename(identity), ".-")
	if safe == "" || safe == "." || safe == ".." || safe == "unnamed" {
		safe = "id"
	}
	if len(safe) > 8 {
		safe = safe[:8]
	}
	return safe + "-" + fmt.Sprintf("%x", sum[:])
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

// shortID returns a short, filesystem-safe ID fragment for disambiguating names.
// It falls back to a sanitized form when the ID is short or non-hex.
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
