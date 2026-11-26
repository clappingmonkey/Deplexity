package api

import (
	"context"
	"fmt"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// enrichSkill converts a skill summary into a full domain Skill by fetching its
// detail (metadata + pre-signed body URL) and downloading the SKILL.md body.
//
// It is fail-soft: a non-fatal failure to fetch detail or download the body
// degrades to the metadata already known from the summary and is recorded in
// issues, so one bad skill never drops the others. Only a fatal error (see
// IsFatal) is returned; it must abort the surrounding enumeration rather than
// be mistaken for a skill that simply has no body.
//
// ownerLabel describes the context the skill was found in (e.g. `space "Foo"`
// or "account") and is used only to identify the failure.
func enrichSkill(ctx context.Context, c enricher, s SkillSummary, ownerLabel string, issues *Issues) (models.Skill, error) {
	skill := models.Skill{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		Scope:       s.Scope,
	}
	item := fmt.Sprintf("skill %q in %s", s.Name, ownerLabel)

	// Fetch full detail (metadata + pre-signed body URL).
	detail, err := GetSkillDetail(ctx, c, s.ID)
	if err != nil {
		if IsFatal(err) {
			return models.Skill{}, err
		}
		issues.Add(models.SectionSkillDetail, item, err)
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
			if IsFatal(err) {
				return models.Skill{}, err
			}
			issues.Add(models.SectionSkillBody, item, err)
		} else {
			skill.Body = string(body)
		}
	}

	return skill, nil
}

// enrichSkills enriches the summaries with the given scope, skipping repeated
// skill IDs so a skill listed on two pages is not downloaded, written and
// counted twice.
func enrichSkills(ctx context.Context, c enricher, summaries []SkillSummary, scope, ownerLabel string, issues *Issues) ([]models.Skill, error) {
	var skills []models.Skill
	seen := map[string]bool{}
	for _, s := range summaries {
		if s.Scope != scope {
			continue
		}
		if s.ID != "" {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
		}
		skill, err := enrichSkill(ctx, c, s, ownerLabel, issues)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

// ListGlobalSkills fetches account-wide skills (scope == "global") and enriches
// each with its detail and SKILL.md body.
//
// Global skills apply to every request regardless of space. They are listed via
// GET /rest/skills/selectable with no collection_uuid, which returns only the
// global-scoped skills; other scopes are filtered out defensively. Enrichment
// is best-effort: a per-skill failure is recorded in issues and the skill is
// retained as metadata-only. Fatal errors abort and propagate.
func ListGlobalSkills(ctx context.Context, c enricher, issues *Issues) ([]models.Skill, error) {
	summaries, err := ListSpaceSkills(ctx, c, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list global skills: %w", err)
	}
	return enrichSkills(ctx, c, summaries, "global", "account", issues)
}

// GetAccount fetches account-wide data. It currently gathers global skills; the
// Account wrapper leaves room for future account-scoped resources without
// reshaping the export surface.
func GetAccount(ctx context.Context, c enricher, issues *Issues) (*models.Account, error) {
	skills, err := ListGlobalSkills(ctx, c, issues)
	if err != nil {
		return nil, err
	}
	return &models.Account{GlobalSkills: skills}, nil
}
