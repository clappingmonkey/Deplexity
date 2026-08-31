package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"

	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/models"
)

// isCancellation reports whether err was caused by context cancellation or
// deadline expiry, which must abort enrichment rather than be logged and
// skipped like ordinary fail-soft errors.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// rawGetter fetches an absolute URL and returns the raw body. It is satisfied
// by *client.Client via GetRawURL and is used to download skill bodies from
// short-lived pre-signed URLs.
type rawGetter interface {
	GetRawURL(context.Context, string) ([]byte, error)
}

// enricher is the client capability set needed to enrich spaces: JSON GETs for
// detail/skill endpoints plus raw fetches for skill bodies.
type enricher interface {
	getter
	rawGetter
}

// ListCollections fetches all user spaces/collections via GET /rest/spaces and
// enriches each with per-space detail (instructions, description, suggested
// queries, primers) and attached skills.
//
// The list endpoint omits all per-space configuration, so each space is
// enriched with a GET /rest/collections/get_collection call and its
// collection-scoped skills. Enrichment is best-effort: a failure to enrich one
// space (or one of its skills) is logged and skipped rather than aborting the
// whole export, so a single broken space never loses the rest of the data.
func ListCollections(ctx context.Context, c *client.Client) ([]models.Space, error) {
	var raw SpacesResponse
	path := fmt.Sprintf("/rest/spaces?version=%s&source=default", apiVersion)
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("failed to list spaces: %w", err)
	}

	// Combine all space categories into a single list.
	allItems := make([]SpaceItem, 0)
	allItems = append(allItems, raw.PrivateSpaces...)
	allItems = append(allItems, raw.SharedSpaces...)
	allItems = append(allItems, raw.InvitedSpaces...)
	allItems = append(allItems, raw.SavedSpaces...)
	allItems = append(allItems, raw.OrganizationSpaces...)

	return enrichSpaces(ctx, c, allItems)
}

// enrichSpaces converts raw space list items into domain models and enriches
// each with per-space detail and collection-scoped skills.
//
// Enrichment is best-effort: a failure to enrich one space (or one of its
// skills) is logged and skipped rather than aborting the whole export, so a
// single broken space never loses the rest of the data. Context cancellation
// is the sole exception — it aborts immediately and propagates the error so an
// interrupted export is not mistaken for a partial success.
//
// It is separated from ListCollections so the fail-soft/cancellation contract
// can be exercised against a mock enricher without a live client.
func enrichSpaces(ctx context.Context, c enricher, items []SpaceItem) ([]models.Space, error) {
	var spaces []models.Space

	for _, item := range items {
		space := models.Space{
			UUID:      item.UUID,
			Name:      item.Title,
			Slug:      item.Slug,
			Emoji:     item.Emoji,
			UpdatedAt: parseTime(item.Updated),
		}

		// Enrich with per-space detail (fail-soft).
		if err := enrichSpaceDetail(ctx, c, &space); err != nil {
			// Context cancellation should propagate; other errors are skipped.
			if isCancellation(err) {
				return nil, err
			}
			log.Printf("warning: could not fetch details for space %q (%s): %v", space.Name, space.Slug, err)
		}

		// Enrich with collection-scoped skills (fail-soft).
		if err := enrichSpaceSkills(ctx, c, &space); err != nil {
			if isCancellation(err) {
				return nil, err
			}
			log.Printf("warning: could not fetch skills for space %q (%s): %v", space.Name, space.Slug, err)
		}

		spaces = append(spaces, space)
	}

	return spaces, nil
}

// GetCollection fetches per-space detail via GET /rest/collections/get_collection.
//
// The endpoint requires collection_slug — passing collection_uuid returns 422.
func GetCollection(ctx context.Context, c getter, slug string) (*CollectionDetailResponse, error) {
	var detail CollectionDetailResponse
	path := fmt.Sprintf(
		"/rest/collections/get_collection?collection_slug=%s&version=%s&source=default",
		url.QueryEscape(slug), apiVersion,
	)
	if err := c.Get(ctx, path, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

// ListSpaceSkills fetches the skills selectable within a space via
// GET /rest/skills/selectable?collection_uuid={uuid}, following next_cursor
// pagination so that spaces with many skills are fully captured.
//
// When collectionUUID is empty the collection_uuid parameter is omitted, which
// the API interprets as "list account-wide skills": the response then carries
// only scope=="global" skills. ListGlobalSkills relies on this behaviour.
func ListSpaceSkills(ctx context.Context, c getter, collectionUUID string) ([]SkillSummary, error) {
	var all []SkillSummary
	cursor := ""

	// Guard against a non-advancing cursor from the reverse-engineered API.
	seenCursors := map[string]bool{}

	for {
		path := fmt.Sprintf(
			"/rest/skills/selectable?version=%s&source=default",
			apiVersion,
		)
		if collectionUUID != "" {
			path += "&collection_uuid=" + url.QueryEscape(collectionUUID)
		}
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}

		var resp SkillsSelectableResponse
		if err := c.Get(ctx, path, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Skills...)

		if resp.NextCursor == nil || *resp.NextCursor == "" {
			break
		}
		if seenCursors[*resp.NextCursor] {
			// The cursor is repeating; stop rather than loop forever.
			owner := collectionUUID
			if owner == "" {
				owner = "global"
			}
			log.Printf("warning: skills pagination cursor repeated for %s; stopping", owner)
			break
		}
		seenCursors[*resp.NextCursor] = true
		cursor = *resp.NextCursor
	}

	return all, nil
}

// GetSkillDetail fetches full skill detail via GET /rest/skills/{id}. The
// returned Skill.FileURL is a short-lived pre-signed URL for the SKILL.md body.
func GetSkillDetail(ctx context.Context, c getter, skillID string) (*SkillDetailResponse, error) {
	var detail SkillDetailResponse
	path := fmt.Sprintf(
		"/rest/skills/%s?view_scope=individual&version=%s&source=default",
		url.PathEscape(skillID), apiVersion,
	)
	if err := c.Get(ctx, path, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

// enrichSpaceDetail populates a space with detail from get_collection.
func enrichSpaceDetail(ctx context.Context, c getter, space *models.Space) error {
	// get_collection is keyed by slug; without one there is nothing to fetch.
	if space.Slug == "" {
		return nil
	}

	detail, err := GetCollection(ctx, c, space.Slug)
	if err != nil {
		return err
	}

	space.Description = detail.Description
	space.Instructions = detail.Instructions
	space.URL = detail.URL
	space.KnowledgeDreamInstructions = detail.KnowledgeDreamInstructions
	space.ProjectStatusSummaryInstruction = detail.ProjectStatusSummaryInstruction
	space.FocusedWebConfig = detail.FocusedWebConfig
	space.MemoryMode = detail.MemoryMode
	space.Access = detail.Access
	space.UserPermission = detail.UserPermission
	space.ThreadCount = detail.ThreadCount
	space.PageCount = detail.PageCount
	space.FileCount = detail.FileCount

	for _, sq := range detail.SuggestedQueries {
		space.SuggestedQueries = append(space.SuggestedQueries, sq.Query)
	}
	for _, p := range detail.Primers {
		space.Primers = append(space.Primers, models.Primer{
			PrimerType: p.PrimerType,
			Queries:    p.Queries,
		})
	}

	return nil
}

// enrichSpaceSkills populates a space with its collection-scoped skills,
// including each skill's SKILL.md body. Per-skill failures are logged and
// skipped so one bad skill does not drop the others.
//
// Only skills with scope=="collection" (space-specific) are attached to a
// space. Account-wide skills (scope=="global") apply to every request
// regardless of space and are exported once at the account level via
// GetAccount/ListGlobalSkills, so they are deliberately skipped here.
func enrichSpaceSkills(ctx context.Context, c enricher, space *models.Space) error {
	// skills/selectable is keyed by collection UUID; without one there is
	// nothing to fetch.
	if space.UUID == "" {
		return nil
	}

	summaries, err := ListSpaceSkills(ctx, c, space.UUID)
	if err != nil {
		return err
	}

	for _, s := range summaries {
		if s.Scope != "collection" {
			continue
		}

		skill, err := enrichSkill(ctx, c, s, fmt.Sprintf("space %q", space.Name))
		if err != nil {
			// Only cancellation is returned; other failures degrade to
			// metadata-only and are already logged.
			return err
		}

		space.Skills = append(space.Skills, skill)
	}

	return nil
}
