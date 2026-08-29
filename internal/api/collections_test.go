package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// mockEnricher is a test double for the enricher interface. It routes GET
// requests to canned JSON responses keyed by a substring of the request path,
// and serves raw URL fetches from a map keyed by URL.
type mockEnricher struct {
	// responses maps a path substring to a JSON body to unmarshal into dest.
	responses map[string]string
	// rawBodies maps an absolute URL to its raw body.
	rawBodies map[string]string
	// getErr, if set for a matching path substring, is returned instead.
	getErrs map[string]error
	// rawErrs, if set for a matching URL, is returned instead.
	rawErrs map[string]error

	paths   []string
	rawURLs []string
}

func (m *mockEnricher) Get(_ context.Context, path string, dest any) error {
	m.paths = append(m.paths, path)
	for sub, err := range m.getErrs {
		if strings.Contains(path, sub) {
			return err
		}
	}
	for sub, body := range m.responses {
		if strings.Contains(path, sub) {
			return json.Unmarshal([]byte(body), dest)
		}
	}
	return errors.New("no canned response for path: " + path)
}

func (m *mockEnricher) GetRawURL(_ context.Context, rawURL string) ([]byte, error) {
	m.rawURLs = append(m.rawURLs, rawURL)
	if err, ok := m.rawErrs[rawURL]; ok {
		return nil, err
	}
	if body, ok := m.rawBodies[rawURL]; ok {
		return []byte(body), nil
	}
	return nil, errors.New("no canned body for url: " + rawURL)
}

func TestGetCollectionUsesSlugParam(t *testing.T) {
	m := &mockEnricher{responses: map[string]string{
		"get_collection": `{"uuid":"u1","instructions":"Test for the deplexity tool","description":"desc"}`,
	}}

	detail, err := GetCollection(context.Background(), m, "recipes-55F50RUIQUK_fqfJUieN1w")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if detail.Instructions != "Test for the deplexity tool" {
		t.Errorf("Instructions = %q, want sentinel", detail.Instructions)
	}
	if len(m.paths) != 1 {
		t.Fatalf("made %d requests, want 1", len(m.paths))
	}
	// The endpoint must be queried by collection_slug (collection_uuid → 422).
	if !strings.Contains(m.paths[0], "collection_slug=recipes-55F50RUIQUK_fqfJUieN1w") {
		t.Errorf("path = %q, want collection_slug param", m.paths[0])
	}
	if strings.Contains(m.paths[0], "collection_uuid=") {
		t.Errorf("path = %q, must not use collection_uuid", m.paths[0])
	}
}

func TestEnrichSpaceDetailMapsFields(t *testing.T) {
	body := `{
		"uuid":"u1",
		"description":"A cooking space",
		"instructions":"Test for the deplexity tool",
		"url":"https://www.perplexity.ai/spaces/recipes",
		"suggested_queries":[{"query":"How do I sear steak?"},{"query":"Substitute for butter"}],
		"primers":[{"primer_type":"welcome","queries":["hi","hello"]}],
		"thread_count":5,"page_count":2,"file_count":1,
		"memory_mode":"auto"
	}`
	m := &mockEnricher{responses: map[string]string{"get_collection": body}}

	space := &models.Space{UUID: "u1", Slug: "recipes-x"}
	if err := enrichSpaceDetail(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceDetail: %v", err)
	}

	if space.Instructions != "Test for the deplexity tool" {
		t.Errorf("Instructions = %q", space.Instructions)
	}
	if space.Description != "A cooking space" {
		t.Errorf("Description = %q", space.Description)
	}
	wantQueries := []string{"How do I sear steak?", "Substitute for butter"}
	if len(space.SuggestedQueries) != len(wantQueries) {
		t.Fatalf("SuggestedQueries = %v, want %v", space.SuggestedQueries, wantQueries)
	}
	for i, q := range wantQueries {
		if space.SuggestedQueries[i] != q {
			t.Errorf("SuggestedQueries[%d] = %q, want %q", i, space.SuggestedQueries[i], q)
		}
	}
	if len(space.Primers) != 1 || space.Primers[0].PrimerType != "welcome" {
		t.Fatalf("Primers = %+v, want one welcome primer", space.Primers)
	}
	if len(space.Primers[0].Queries) != 2 {
		t.Errorf("Primer queries = %v, want 2", space.Primers[0].Queries)
	}
	if space.ThreadCount != 5 || space.PageCount != 2 || space.FileCount != 1 {
		t.Errorf("counts = %d/%d/%d, want 5/2/1", space.ThreadCount, space.PageCount, space.FileCount)
	}
	if space.MemoryMode != "auto" {
		t.Errorf("MemoryMode = %q, want auto", space.MemoryMode)
	}
}

func TestEnrichSpaceSkillsFiltersCollectionScopeAndFetchesBody(t *testing.T) {
	// selectable returns one collection-scoped skill and one global skill;
	// only the collection-scoped one must be exported.
	selectable := `{"skills":[
		{"id":"skill-collection","name":"git-commit","description":"Commit helper","scope":"collection"},
		{"id":"skill-global","name":"create-skill","description":"Global helper","scope":"global"}
	],"next_cursor":null}`
	detail := `{"is_owner":false,"is_creator":true,"enabled":true,"installed":true,"skill":{
		"id":"skill-collection","name":"git-commit","description":"Commit helper","scope":"collection",
		"file_url":"https://s3.example.com/git-commit/SKILL.md?sig=abc",
		"categories":["dev"],"tags":{"hide_from_index":"false"},
		"created_at":"2026-08-29T12:11:27.702265","updated_at":"2026-08-29T12:11:27.865141"
	}}`
	m := &mockEnricher{
		responses: map[string]string{
			"selectable":      selectable,
			"skills/skill-collection": detail,
		},
		rawBodies: map[string]string{
			"https://s3.example.com/git-commit/SKILL.md?sig=abc": "---\nname: git-commit\n---\nbody",
		},
	}

	space := &models.Space{UUID: "u1", Name: "Recipes"}
	if err := enrichSpaceSkills(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceSkills: %v", err)
	}

	if len(space.Skills) != 1 {
		t.Fatalf("got %d skills, want 1 (collection-scoped only)", len(space.Skills))
	}
	sk := space.Skills[0]
	if sk.ID != "skill-collection" || sk.Name != "git-commit" {
		t.Errorf("skill = %+v, want git-commit collection skill", sk)
	}
	if sk.Scope != "collection" {
		t.Errorf("scope = %q, want collection", sk.Scope)
	}
	if sk.Body != "---\nname: git-commit\n---\nbody" {
		t.Errorf("body = %q, want fetched SKILL.md", sk.Body)
	}
	if len(sk.Categories) != 1 || sk.Categories[0] != "dev" {
		t.Errorf("categories = %v, want [dev]", sk.Categories)
	}
	if sk.Tags["hide_from_index"] != "false" {
		t.Errorf("tags = %v, want hide_from_index=false", sk.Tags)
	}
}

func TestEnrichSpaceSkillsFailSoftOnBodyDownload(t *testing.T) {
	selectable := `{"skills":[
		{"id":"skill-collection","name":"git-commit","description":"Commit helper","scope":"collection"}
	],"next_cursor":null}`
	detail := `{"skill":{"id":"skill-collection","name":"git-commit","scope":"collection",
		"file_url":"https://s3.example.com/broken/SKILL.md"}}`
	m := &mockEnricher{
		responses: map[string]string{
			"selectable":              selectable,
			"skills/skill-collection": detail,
		},
		rawErrs: map[string]error{
			"https://s3.example.com/broken/SKILL.md": errors.New("403 expired"),
		},
	}

	space := &models.Space{UUID: "u1", Name: "Recipes"}
	if err := enrichSpaceSkills(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceSkills should be fail-soft on body error, got: %v", err)
	}
	// The skill is still exported as metadata, just without a body.
	if len(space.Skills) != 1 {
		t.Fatalf("got %d skills, want 1 (metadata retained)", len(space.Skills))
	}
	if space.Skills[0].Body != "" {
		t.Errorf("body = %q, want empty after failed download", space.Skills[0].Body)
	}
}

func TestEnrichSpaceSkillsFailSoftOnDetailError(t *testing.T) {
	// A non-cancellation error fetching skill detail must not abort the whole
	// enrichment: the skill is retained as metadata-only from the summary.
	selectable := `{"skills":[
		{"id":"skill-collection","name":"git-commit","description":"Commit helper","scope":"collection"}
	],"next_cursor":null}`
	m := &mockEnricher{
		responses: map[string]string{"selectable": selectable},
		getErrs:   map[string]error{"skills/skill-collection": errors.New("500 server error")},
	}

	space := &models.Space{UUID: "u1", Name: "Recipes"}
	if err := enrichSpaceSkills(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceSkills should be fail-soft on detail error, got: %v", err)
	}
	if len(space.Skills) != 1 {
		t.Fatalf("got %d skills, want 1 (metadata retained)", len(space.Skills))
	}
	sk := space.Skills[0]
	if sk.ID != "skill-collection" || sk.Name != "git-commit" || sk.Description != "Commit helper" {
		t.Errorf("skill = %+v, want summary metadata retained", sk)
	}
	if sk.Body != "" {
		t.Errorf("body = %q, want empty (detail never fetched)", sk.Body)
	}
	// No body download should have been attempted.
	if len(m.rawURLs) != 0 {
		t.Errorf("attempted %d raw fetches, want 0", len(m.rawURLs))
	}
}

// pagingGetter serves /rest/skills/selectable across multiple pages driven by a
// cursor query param, and returns any other path from a static map.
type pagingGetter struct {
	// pages is indexed by the cursor value ("" for the first page).
	pages  map[string]string
	static map[string]string
	paths  []string
}

func (p *pagingGetter) Get(_ context.Context, path string, dest any) error {
	p.paths = append(p.paths, path)
	if strings.Contains(path, "selectable") {
		cursor := ""
		if i := strings.Index(path, "cursor="); i >= 0 {
			cursor = path[i+len("cursor="):]
			if amp := strings.IndexByte(cursor, '&'); amp >= 0 {
				cursor = cursor[:amp]
			}
		}
		body, ok := p.pages[cursor]
		if !ok {
			return errors.New("no page for cursor: " + cursor)
		}
		return json.Unmarshal([]byte(body), dest)
	}
	for sub, body := range p.static {
		if strings.Contains(path, sub) {
			return json.Unmarshal([]byte(body), dest)
		}
	}
	return errors.New("no canned response for path: " + path)
}

func TestListSpaceSkillsFollowsPagination(t *testing.T) {
	p := &pagingGetter{pages: map[string]string{
		"": `{"skills":[{"id":"s1","name":"one","scope":"collection"}],"next_cursor":"CUR2"}`,
		"CUR2": `{"skills":[{"id":"s2","name":"two","scope":"global"}],"next_cursor":""}`,
	}}

	skills, err := ListSpaceSkills(context.Background(), p, "collection-uuid")
	if err != nil {
		t.Fatalf("ListSpaceSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2 across both pages", len(skills))
	}
	if skills[0].ID != "s1" || skills[1].ID != "s2" {
		t.Errorf("skills = %+v, want s1 then s2", skills)
	}
	// Second request must carry the cursor from the first page.
	if len(p.paths) != 2 {
		t.Fatalf("made %d requests, want 2", len(p.paths))
	}
	if !strings.Contains(p.paths[1], "cursor=CUR2") {
		t.Errorf("second path = %q, want cursor=CUR2", p.paths[1])
	}
}

func TestListSpaceSkillsStopsOnRepeatedCursor(t *testing.T) {
	// A misbehaving API that keeps returning the same cursor must not loop
	// forever.
	p := &pagingGetter{pages: map[string]string{
		"":     `{"skills":[{"id":"s1","name":"one","scope":"collection"}],"next_cursor":"LOOP"}`,
		"LOOP": `{"skills":[{"id":"s2","name":"two","scope":"collection"}],"next_cursor":"LOOP"}`,
	}}

	skills, err := ListSpaceSkills(context.Background(), p, "collection-uuid")
	if err != nil {
		t.Fatalf("ListSpaceSkills: %v", err)
	}
	// First page + one LOOP page, then the repeated cursor stops iteration.
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2 before loop guard trips", len(skills))
	}
}

func TestEnrichSpaceSkillsPropagatesCancellation(t *testing.T) {
	selectable := `{"skills":[
		{"id":"skill-collection","name":"git-commit","scope":"collection"}
	],"next_cursor":null}`
	m := &mockEnricher{
		responses: map[string]string{"selectable": selectable},
		getErrs:   map[string]error{"skills/skill-collection": context.Canceled},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	space := &models.Space{UUID: "u1", Name: "Recipes"}
	err := enrichSpaceSkills(ctx, m, space)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled to propagate", err)
	}
}

func TestEnrichSpaceDetailSkipsEmptySlug(t *testing.T) {
	m := &mockEnricher{} // no canned responses: any Get would error
	space := &models.Space{UUID: "u1", Name: "Recipes"} // Slug == ""

	if err := enrichSpaceDetail(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceDetail with empty slug = %v, want nil (skip)", err)
	}
	if len(m.paths) != 0 {
		t.Errorf("issued %d requests for a slugless space, want 0", len(m.paths))
	}
}

func TestEnrichSpaceSkillsSkipsEmptyUUID(t *testing.T) {
	m := &mockEnricher{} // no canned responses: any Get would error
	space := &models.Space{Slug: "recipes", Name: "Recipes"} // UUID == ""

	if err := enrichSpaceSkills(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceSkills with empty uuid = %v, want nil (skip)", err)
	}
	if len(m.paths) != 0 {
		t.Errorf("issued %d requests for a uuidless space, want 0", len(m.paths))
	}
	if len(space.Skills) != 0 {
		t.Errorf("got %d skills for a uuidless space, want 0", len(space.Skills))
	}
}

func TestEnrichSpaceSkillsEmptyFileURLSkipsBodyFetch(t *testing.T) {
	// A skill whose detail carries no file_url is exported as metadata only,
	// and no body download is attempted.
	selectable := `{"skills":[
		{"id":"skill-collection","name":"git-commit","scope":"collection"}
	],"next_cursor":null}`
	detail := `{"skill":{"id":"skill-collection","name":"git-commit","scope":"collection","file_url":""}}`
	m := &mockEnricher{
		responses: map[string]string{
			"selectable":              selectable,
			"skills/skill-collection": detail,
		},
	}

	space := &models.Space{UUID: "u1", Name: "Recipes"}
	if err := enrichSpaceSkills(context.Background(), m, space); err != nil {
		t.Fatalf("enrichSpaceSkills: %v", err)
	}
	if len(space.Skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(space.Skills))
	}
	if space.Skills[0].Body != "" {
		t.Errorf("body = %q, want empty (no file_url)", space.Skills[0].Body)
	}
	if len(m.rawURLs) != 0 {
		t.Errorf("attempted %d raw fetches, want 0 (empty file_url)", len(m.rawURLs))
	}
}

func TestEnrichSpacesFailSoftAcrossSpaces(t *testing.T) {
	// Three spaces: the first has a failing detail fetch, the second a failing
	// skills fetch, the third succeeds. All three must still be returned and
	// the call must not error (fail-soft).
	m := &mockEnricher{
		responses: map[string]string{
			"get_collection": `{"instructions":"ok"}`,
			"selectable":     `{"skills":[],"next_cursor":null}`,
		},
		getErrs: map[string]error{
			// Path substrings are matched before responses; scope the failures
			// narrowly so only the intended space/endpoint is affected.
			"collection_slug=bad-detail": errors.New("500 detail"),
		},
	}

	items := []SpaceItem{
		{UUID: "u1", Title: "BadDetail", Slug: "bad-detail"},
		{UUID: "u2", Title: "Good", Slug: "good-slug"},
	}

	spaces, err := enrichSpaces(context.Background(), m, items)
	if err != nil {
		t.Fatalf("enrichSpaces should be fail-soft, got: %v", err)
	}
	if len(spaces) != 2 {
		t.Fatalf("got %d spaces, want 2 (all retained despite one failure)", len(spaces))
	}
	// The failing-detail space still carries its list metadata.
	if spaces[0].Name != "BadDetail" || spaces[0].Instructions != "" {
		t.Errorf("space[0] = %+v, want metadata retained without instructions", spaces[0])
	}
	// The healthy space was enriched.
	if spaces[1].Instructions != "ok" {
		t.Errorf("space[1].Instructions = %q, want enriched", spaces[1].Instructions)
	}
}

func TestEnrichSpacesPropagatesCancellation(t *testing.T) {
	// A cancellation surfacing from any space must abort the whole loop.
	m := &mockEnricher{
		getErrs: map[string]error{"get_collection": context.Canceled},
	}
	items := []SpaceItem{{UUID: "u1", Title: "Recipes", Slug: "recipes"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := enrichSpaces(ctx, m, items); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled to propagate", err)
	}
}
