package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestListSpaceSkillsOmitsCollectionUUIDWhenEmpty(t *testing.T) {
	// An empty collectionUUID must produce a selectable request with no
	// collection_uuid param — the API returns only global skills in that case.
	m := &mockEnricher{responses: map[string]string{
		"selectable": `{"skills":[{"id":"g1","name":"create-skill","scope":"global"}],"next_cursor":null}`,
	}}

	skills, err := ListSpaceSkills(context.Background(), m, "")
	if err != nil {
		t.Fatalf("ListSpaceSkills: %v", err)
	}
	if len(skills) != 1 || skills[0].ID != "g1" {
		t.Fatalf("skills = %+v, want one global skill g1", skills)
	}
	if len(m.paths) != 1 {
		t.Fatalf("made %d requests, want 1", len(m.paths))
	}
	if strings.Contains(m.paths[0], "collection_uuid") {
		t.Errorf("path = %q, must omit collection_uuid for the global list", m.paths[0])
	}
}

func TestListSpaceSkillsIncludesCollectionUUIDWhenSet(t *testing.T) {
	// A non-empty UUID keeps the existing per-space behaviour.
	m := &mockEnricher{responses: map[string]string{
		"selectable": `{"skills":[],"next_cursor":null}`,
	}}

	if _, err := ListSpaceSkills(context.Background(), m, "u1"); err != nil {
		t.Fatalf("ListSpaceSkills: %v", err)
	}
	if len(m.paths) != 1 || !strings.Contains(m.paths[0], "collection_uuid=u1") {
		t.Errorf("path = %q, want collection_uuid=u1", m.paths[0])
	}
}

func TestListGlobalSkillsFiltersGlobalScopeAndFetchesBody(t *testing.T) {
	// selectable (no collection) returns a global skill; it must be enriched
	// with detail + body. A non-global entry, if present, is ignored.
	selectable := `{"skills":[
		{"id":"g1","name":"create-skill","description":"Create skills","scope":"global"},
		{"id":"c1","name":"stray","description":"should be ignored","scope":"collection"}
	],"next_cursor":null}`
	detail := `{"skill":{"id":"g1","name":"create-skill","description":"Create skills","scope":"global",
		"file_url":"https://s3.example.com/create-skill/SKILL.md?sig=abc",
		"categories":["meta"],"tags":{"selectable":"true"},
		"created_at":"2026-02-26T21:59:08","updated_at":"2026-08-27T19:29:45"}}`
	m := &mockEnricher{
		responses: map[string]string{
			"selectable": selectable,
			"skills/g1":  detail,
		},
		rawBodies: map[string]string{
			"https://s3.example.com/create-skill/SKILL.md?sig=abc": "---\nname: create-skill\n---\nbody",
		},
	}

	skills, err := ListGlobalSkills(context.Background(), m)
	if err != nil {
		t.Fatalf("ListGlobalSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1 (global only)", len(skills))
	}
	sk := skills[0]
	if sk.ID != "g1" || sk.Name != "create-skill" || sk.Scope != "global" {
		t.Errorf("skill = %+v, want global create-skill", sk)
	}
	if sk.Body != "---\nname: create-skill\n---\nbody" {
		t.Errorf("body = %q, want fetched SKILL.md", sk.Body)
	}
	if len(sk.Categories) != 1 || sk.Categories[0] != "meta" {
		t.Errorf("categories = %v, want [meta]", sk.Categories)
	}
	// Only the global skill's detail should be fetched (the collection stray
	// is filtered before enrichment).
	for _, p := range m.paths {
		if strings.Contains(p, "skills/c1") {
			t.Errorf("fetched detail for filtered collection skill: %q", p)
		}
	}
}

func TestListGlobalSkillsFailSoftOnBodyDownload(t *testing.T) {
	selectable := `{"skills":[{"id":"g1","name":"create-skill","scope":"global"}],"next_cursor":null}`
	detail := `{"skill":{"id":"g1","name":"create-skill","scope":"global",
		"file_url":"https://s3.example.com/broken/SKILL.md"}}`
	m := &mockEnricher{
		responses: map[string]string{"selectable": selectable, "skills/g1": detail},
		rawErrs:   map[string]error{"https://s3.example.com/broken/SKILL.md": errors.New("403 expired")},
	}

	skills, err := ListGlobalSkills(context.Background(), m)
	if err != nil {
		t.Fatalf("ListGlobalSkills should be fail-soft on body error, got: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1 (metadata retained)", len(skills))
	}
	if skills[0].Body != "" {
		t.Errorf("body = %q, want empty after failed download", skills[0].Body)
	}
}

func TestListGlobalSkillsFailSoftOnDetailError(t *testing.T) {
	selectable := `{"skills":[{"id":"g1","name":"create-skill","description":"Create skills","scope":"global"}],"next_cursor":null}`
	m := &mockEnricher{
		responses: map[string]string{"selectable": selectable},
		getErrs:   map[string]error{"skills/g1": errors.New("500 server error")},
	}

	skills, err := ListGlobalSkills(context.Background(), m)
	if err != nil {
		t.Fatalf("ListGlobalSkills should be fail-soft on detail error, got: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1 (metadata retained)", len(skills))
	}
	if skills[0].ID != "g1" || skills[0].Description != "Create skills" {
		t.Errorf("skill = %+v, want summary metadata retained", skills[0])
	}
	if skills[0].Body != "" {
		t.Errorf("body = %q, want empty (detail never fetched)", skills[0].Body)
	}
}

func TestListGlobalSkillsPropagatesCancellation(t *testing.T) {
	selectable := `{"skills":[{"id":"g1","name":"create-skill","scope":"global"}],"next_cursor":null}`
	m := &mockEnricher{
		responses: map[string]string{"selectable": selectable},
		getErrs:   map[string]error{"skills/g1": context.Canceled},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListGlobalSkills(ctx, m); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled to propagate", err)
	}
}

func TestGetAccountWrapsGlobalSkills(t *testing.T) {
	selectable := `{"skills":[{"id":"g1","name":"create-skill","scope":"global"}],"next_cursor":null}`
	detail := `{"skill":{"id":"g1","name":"create-skill","scope":"global","file_url":""}}`
	m := &mockEnricher{
		responses: map[string]string{"selectable": selectable, "skills/g1": detail},
	}

	account, err := GetAccount(context.Background(), m)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	var _ *models.Account = account // GetAccount must return *models.Account
	if account == nil {
		t.Fatal("account is nil")
	}
	if len(account.GlobalSkills) != 1 || account.GlobalSkills[0].ID != "g1" {
		t.Errorf("GlobalSkills = %+v, want one skill g1", account.GlobalSkills)
	}
}

// pagingEnricher adds a no-op GetRawURL to pagingGetter so it satisfies the
// enricher interface required by ListGlobalSkills. Bodies are never exercised
// here (the skills under test carry no file_url), so an empty body suffices.
type pagingEnricher struct {
	*pagingGetter
}

func (pagingEnricher) GetRawURL(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func TestListGlobalSkillsDeduplicatesAcrossPages(t *testing.T) {
	// The same global skill appears on both pages; it must be enriched,
	// returned, and counted only once.
	p := &pagingEnricher{&pagingGetter{
		pages: map[string]string{
			"":     `{"skills":[{"id":"g1","name":"create-skill","scope":"global"}],"next_cursor":"CUR2"}`,
			"CUR2": `{"skills":[{"id":"g1","name":"create-skill","scope":"global"}],"next_cursor":""}`,
		},
		static: map[string]string{
			"skills/g1": `{"skill":{"id":"g1","name":"create-skill","scope":"global","file_url":""}}`,
		},
	}}

	skills, err := ListGlobalSkills(context.Background(), p)
	if err != nil {
		t.Fatalf("ListGlobalSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1 (deduplicated across pages)", len(skills))
	}
}

func TestListGlobalSkillsPropagatesCancellationFromList(t *testing.T) {
	// Cancellation surfacing from the selectable list call itself (not the
	// per-skill detail fetch) must abort and propagate.
	m := &mockEnricher{
		getErrs: map[string]error{"selectable": context.Canceled},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListGlobalSkills(ctx, m); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled to propagate", err)
	}
}

func TestGetAccountEmptyWhenNoGlobalSkills(t *testing.T) {
	m := &mockEnricher{responses: map[string]string{
		"selectable": `{"skills":[],"next_cursor":null}`,
	}}

	account, err := GetAccount(context.Background(), m)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if account == nil {
		t.Fatal("account is nil, want non-nil with empty skills")
	}
	if len(account.GlobalSkills) != 0 {
		t.Errorf("GlobalSkills = %+v, want empty", account.GlobalSkills)
	}
}
