package api

import (
	"context"
	"fmt"
	"log"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// enrichSkill converts a skill summary into a full domain Skill by fetching its
// detail (metadata + pre-signed body URL) and downloading the SKILL.md body.
//
// It is fail-soft: a non-cancellation failure to fetch detail or download the
// body degrades to the metadata already known from the summary (logged, never
// fatal), so one bad skill never drops the others. Context cancellation is the
// sole error returned — it must abort the surrounding enumeration rather than
// be mistaken for a skill that simply has no body.
//
// ownerLabel describes the context the skill was found in (e.g. `space "Foo"`
// or "account") and is used only in warning messages.
func enrichSkill(ctx context.Context, c enricher, s SkillSummary, ownerLabel string) (models.Skill, error) {
	skill := models.Skill{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		Scope:       s.Scope,
	}

	// Fetch full detail (metadata + pre-signed body URL).
	detail, err := GetSkillDetail(ctx, c, s.ID)
	if err != nil {
		if isCancellation(err) {
			return models.Skill{}, err
		}
		log.Printf("warning: could not fetch detail for skill %q in %s: %v", s.Name, ownerLabel, err)
		return skill, nil
	}

	skill.Categories = detail.Skill.Categories
	skill.Tags = detail.Skill.Tags
	skill.CreatedAt = detail.Skill.CreatedAt
	skill.UpdatedAt = detail.Skill.UpdatedAt

	// Download the SKILL.md body now — the URL is short-lived (~15 min).
	if detail.Skill.FileURL != "" {
		body, err := c.GetRawURL(ctx, detail.Skill.FileURL)
		if err != nil {
			if isCancellation(err) {
				return models.Skill{}, err
			}
			log.Printf("warning: could not download body for skill %q in %s: %v", s.Name, ownerLabel, err)
		} else {
			skill.Body = string(body)
		}
	}

	return skill, nil
}

// ListGlobalSkills fetches account-wide skills (scope == "global") and enriches
// each with its detail and SKILL.md body.
//
// Global skills apply to every request regardless of space. They are listed via
// GET /rest/skills/selectable with no collection_uuid, which returns only the
// global-scoped skills. Enrichment is best-effort: a per-skill failure is
// logged and the skill is retained as metadata-only, so one broken skill never
// drops the rest. Context cancellation aborts and propagates.
func ListGlobalSkills(ctx context.Context, c enricher) ([]models.Skill, error) {
	summaries, err := ListSpaceSkills(ctx, c, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list global skills: %w", err)
	}

	var skills []models.Skill
	seen := map[string]bool{}
	for _, s := range summaries {
		// The selectable endpoint without a collection_uuid should only return
		// global skills, but filter defensively in case the API interleaves
		// other scopes.
		if s.Scope != "global" {
			continue
		}
		// Guard against the paginated list returning the same skill twice,
		// which would otherwise be enriched, written, and counted twice.
		if s.ID != "" && seen[s.ID] {
			continue
		}
		seen[s.ID] = true

		skill, err := enrichSkill(ctx, c, s, "account")
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}

	return skills, nil
}

// GetAccount fetches account-wide data. It currently gathers global skills; the
// Account wrapper leaves room for future account-scoped resources without
// reshaping the export surface.
func GetAccount(ctx context.Context, c enricher) (*models.Account, error) {
	skills, err := ListGlobalSkills(ctx, c)
	if err != nil {
		return nil, err
	}
	return &models.Account{GlobalSkills: skills}, nil
}
