package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/export"
	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestNeedsThreadFetch(t *testing.T) {
	previous := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	current := previous.Add(time.Hour)
	cached := &models.Thread{Complete: true}

	tests := []struct {
		name    string
		refresh bool
		ref     models.ThreadRef
		cached  *models.Thread
		err     error
		want    bool
	}{
		{name: "missing cached detail", err: errors.New("not found"), want: true},
		{name: "unchanged without refresh", ref: models.ThreadRef{UpdatedAt: current, PreviousUpdatedAt: previous}, cached: cached, want: false},
		{name: "unchanged with refresh", refresh: true, ref: models.ThreadRef{UpdatedAt: previous, PreviousUpdatedAt: previous}, cached: cached, want: false},
		{name: "newer with refresh", refresh: true, ref: models.ThreadRef{UpdatedAt: current, PreviousUpdatedAt: previous}, cached: cached, want: true},
		{name: "unknown current timestamp", refresh: true, ref: models.ThreadRef{PreviousUpdatedAt: previous}, cached: cached, want: true},
		{name: "legacy index timestamp", refresh: true, ref: models.ThreadRef{UpdatedAt: current}, cached: cached, want: true},
		{name: "nil cached thread", refresh: true, ref: models.ThreadRef{UpdatedAt: current, PreviousUpdatedAt: previous}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsThreadFetch(tt.refresh, tt.ref, tt.cached, tt.err); got != tt.want {
				t.Errorf("needsThreadFetch() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestResumePoint(t *testing.T) {
	partial := &models.Thread{NextCursor: "c1"}

	tests := []struct {
		name    string
		refresh bool
		partial *models.Thread
		want    bool
	}{
		{name: "interrupted fetch", partial: partial, want: true},
		{name: "refresh discards checkpoint", refresh: true, partial: partial},
		{name: "no partial on disk"},
		{name: "already complete", partial: &models.Thread{Complete: true, NextCursor: "c1"}},
		{name: "no progress recorded", partial: &models.Thread{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resumePoint(tt.refresh, tt.partial)
			if (got != nil) != tt.want {
				t.Errorf("resumePoint() = %v, want non-nil %t", got, tt.want)
			}
		})
	}
}

func TestAttachPreviousUpdatedAt(t *testing.T) {
	previous := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	refs := []models.ThreadRef{{UUID: "unchanged"}, {UUID: "new"}}
	attachPreviousUpdatedAt(refs, []models.ThreadRef{{UUID: "unchanged", UpdatedAt: previous}})

	if !refs[0].PreviousUpdatedAt.Equal(previous) {
		t.Errorf("PreviousUpdatedAt = %v, want %v", refs[0].PreviousUpdatedAt, previous)
	}
	if !refs[1].PreviousUpdatedAt.IsZero() {
		t.Errorf("PreviousUpdatedAt = %v, want zero time", refs[1].PreviousUpdatedAt)
	}
}

func TestIndexServableFromCache(t *testing.T) {
	fresh := time.Now().UTC()
	stale := fresh.Add(-25 * time.Hour)

	tests := []struct {
		name  string
		index *models.ThreadIndex
		want  bool
	}{
		{name: "nil index", index: nil, want: false},
		{name: "complete with threads", index: &models.ThreadIndex{Complete: true, Total: 3, FetchedAt: fresh}, want: true},
		{name: "poisoned empty complete index", index: &models.ThreadIndex{Complete: true, Total: 0, FetchedAt: fresh}, want: false},
		{name: "incomplete index", index: &models.ThreadIndex{Complete: false, Total: 3, FetchedAt: fresh}, want: false},
		{name: "expired cache", index: &models.ThreadIndex{Complete: true, Total: 3, FetchedAt: stale}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := indexServableFromCache(tt.index); got != tt.want {
				t.Errorf("indexServableFromCache() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestFinalizeIndex(t *testing.T) {
	tests := []struct {
		name         string
		total        int
		wantComplete bool
	}{
		{name: "empty result stays incomplete", total: 0, wantComplete: false},
		{name: "populated result is complete", total: 3, wantComplete: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
			index := &models.ThreadIndex{Total: tt.total}

			if err := finalizeIndex(jsonExp, index); err != nil {
				t.Fatalf("finalizeIndex: %v", err)
			}
			if index.Complete != tt.wantComplete {
				t.Errorf("Complete = %t, want %t", index.Complete, tt.wantComplete)
			}
			if index.FetchedAt.IsZero() {
				t.Error("FetchedAt not stamped")
			}

			// The persisted index must round-trip the same completion state, so
			// an empty result cannot be served from cache on the next run.
			reloaded, err := jsonExp.LoadThreadIndex()
			if err != nil {
				t.Fatalf("LoadThreadIndex: %v", err)
			}
			if reloaded == nil {
				t.Fatal("index was not persisted")
			}
			if reloaded.Complete != tt.wantComplete {
				t.Errorf("persisted Complete = %t, want %t", reloaded.Complete, tt.wantComplete)
			}
		})
	}
}

func TestFetchThreadIndexReListsIncompleteCacheFromStart(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	previousUpdatedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	previous := &models.ThreadIndex{
		Threads: []models.ThreadRef{
			{UUID: "old-1", UpdatedAt: previousUpdatedAt, RetryRequired: true},
			{UUID: "stale"},
		},
		Total:    2,
		Complete: false,
	}
	if err := jsonExp.SaveThreadIndex(previous); err != nil {
		t.Fatalf("SaveThreadIndex: %v", err)
	}

	called := false
	list := func(_ context.Context, onProgress func(int)) ([]models.Thread, error) {
		called = true
		if onProgress != nil {
			onProgress(3)
		}
		return []models.Thread{
			{UUID: "promoted", Title: "Promoted"},
			{UUID: "old-1", Title: "Old"},
			{UUID: "tail", Title: "Tail"},
		}, nil
	}

	cmd := &ExportCmd{}
	refs, err := cmd.fetchThreadIndex(context.Background(), jsonExp, list)
	if err != nil {
		t.Fatalf("fetchThreadIndex: %v", err)
	}
	if !called {
		t.Fatal("incomplete cache was served without re-listing")
	}
	wantOrder := []string{"promoted", "old-1", "tail"}
	if got := threadRefUUIDs(refs); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("refs = %v, want %v", got, wantOrder)
	}
	if !refs[1].RetryRequired || !refs[1].PreviousUpdatedAt.Equal(previousUpdatedAt) {
		t.Fatalf("old-1 metadata = %#v, want retry and previous timestamp preserved", refs[1])
	}

	reloaded, err := jsonExp.LoadThreadIndex()
	if err != nil {
		t.Fatalf("LoadThreadIndex: %v", err)
	}
	if !reloaded.Complete || reloaded.Total != 3 {
		t.Fatalf("persisted index = %#v, want complete total 3", reloaded)
	}
	if got := threadRefUUIDs(reloaded.Threads); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("persisted order = %v, want %v", got, wantOrder)
	}
}

func TestFetchThreadIndexServesCompleteCacheWithoutListing(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	index := &models.ThreadIndex{
		Threads:   []models.ThreadRef{{UUID: "cached"}},
		Total:     1,
		FetchedAt: time.Now().UTC(),
		Complete:  true,
	}
	if err := jsonExp.SaveThreadIndex(index); err != nil {
		t.Fatalf("SaveThreadIndex: %v", err)
	}
	list := func(context.Context, func(int)) ([]models.Thread, error) {
		t.Fatal("list called for servable complete cache")
		return nil, nil
	}

	cmd := &ExportCmd{}
	refs, err := cmd.fetchThreadIndex(context.Background(), jsonExp, list)
	if err != nil {
		t.Fatalf("fetchThreadIndex: %v", err)
	}
	if got := threadRefUUIDs(refs); !reflect.DeepEqual(got, []string{"cached"}) {
		t.Fatalf("refs = %v, want cached", got)
	}
}

func TestFetchThreadIndexRefreshBypassesCompleteCacheAndDemotesOnError(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	index := &models.ThreadIndex{
		Threads:   []models.ThreadRef{{UUID: "cached", RetryRequired: true}},
		Total:     1,
		FetchedAt: time.Now().UTC(),
		Complete:  true,
	}
	if err := jsonExp.SaveThreadIndex(index); err != nil {
		t.Fatalf("SaveThreadIndex: %v", err)
	}
	wantErr := errors.New("refresh interrupted")
	called := false
	list := func(context.Context, func(int)) ([]models.Thread, error) {
		called = true
		return []models.Thread{{UUID: "new-head"}}, wantErr
	}

	cmd := &ExportCmd{Refresh: true}
	if _, err := cmd.fetchThreadIndex(context.Background(), jsonExp, list); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want refresh interruption", err)
	}
	if !called {
		t.Fatal("refresh served complete cache without listing")
	}
	reloaded, err := jsonExp.LoadThreadIndex()
	if err != nil {
		t.Fatalf("LoadThreadIndex: %v", err)
	}
	if reloaded.Complete {
		t.Fatal("failed refresh left the previous index complete")
	}
	if got := threadRefUUIDs(reloaded.Threads); !reflect.DeepEqual(got, []string{"new-head", "cached"}) {
		t.Fatalf("persisted refs = %v, want new prefix and prior cache", got)
	}
	if !reloaded.Threads[1].RetryRequired {
		t.Fatal("prior retry metadata was lost during failed refresh")
	}
}

func TestFetchThreadIndexInterruptedRelistRetainsPriorMetadata(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	previous := &models.ThreadIndex{
		Threads: []models.ThreadRef{
			{UUID: "old-head"},
			{UUID: "deep-retry", RetryRequired: true},
		},
		Total:    2,
		Complete: false,
	}
	if err := jsonExp.SaveThreadIndex(previous); err != nil {
		t.Fatalf("SaveThreadIndex: %v", err)
	}
	listErr := errors.New("listing interrupted")
	list := func(context.Context, func(int)) ([]models.Thread, error) {
		return []models.Thread{{UUID: "new-head"}, {UUID: "old-head"}}, listErr
	}

	cmd := &ExportCmd{}
	if _, err := cmd.fetchThreadIndex(context.Background(), jsonExp, list); !errors.Is(err, listErr) {
		t.Fatalf("error = %v, want listing interruption", err)
	}
	reloaded, err := jsonExp.LoadThreadIndex()
	if err != nil {
		t.Fatalf("LoadThreadIndex: %v", err)
	}
	if reloaded.Complete {
		t.Fatal("interrupted re-list was marked complete")
	}
	wantOrder := []string{"new-head", "old-head", "deep-retry"}
	if got := threadRefUUIDs(reloaded.Threads); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("persisted order = %v, want %v", got, wantOrder)
	}
	if !reloaded.Threads[2].RetryRequired {
		t.Fatal("unseen prior retry metadata was lost")
	}
}

func threadRefUUIDs(refs []models.ThreadRef) []string {
	uuids := make([]string, len(refs))
	for i := range refs {
		uuids[i] = refs[i].UUID
	}
	return uuids
}

// TestExportFormatAccountGating locks in the render half of the --no-spaces
// gate: when spaces (and thus global skills) are disabled, Run leaves account
// nil, and exportFormat must then write no account/ output for any format. The
// positive case confirms a non-nil account does produce account/ output.
func TestExportFormatAccountGating(t *testing.T) {
	for _, format := range []string{"json", "markdown"} {
		t.Run(format+"/nil account writes nothing", func(t *testing.T) {
			dir := t.TempDir()
			cmd := &ExportCmd{Output: dir, Format: []string{format}}
			if err := cmd.exportFormat(context.Background(), format, nil, nil, nil, nil); err != nil {
				t.Fatalf("exportFormat: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "account")); !os.IsNotExist(err) {
				t.Errorf("nil account produced account/ output (err=%v)", err)
			}
		})

		t.Run(format+"/non-nil account writes output", func(t *testing.T) {
			dir := t.TempDir()
			cmd := &ExportCmd{Output: dir, Format: []string{format}}
			account := &models.Account{GlobalSkills: []models.Skill{
				{ID: "g1", Name: "create-skill", Scope: "global", Body: "body"},
			}}
			if err := cmd.exportFormat(context.Background(), format, nil, nil, nil, account); err != nil {
				t.Fatalf("exportFormat: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "account")); err != nil {
				t.Errorf("non-nil account produced no account/ output: %v", err)
			}
		})
	}
}

func TestFetchThreadDetailsTracksFailuresAndKeepsSuccessfulThreads(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	refs := []models.ThreadRef{
		{UUID: "thread-1", Title: "First"},
		{UUID: "thread-2", Title: "Second"},
		{UUID: "thread-3", Title: "Third"},
	}
	fetch := func(_ context.Context, uuid string, _ *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
		if uuid == "thread-2" {
			return nil, errors.New("upstream failure")
		}
		return &models.Thread{UUID: uuid, Slug: uuid, Complete: true}, nil
	}

	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(threads) != 2 || threads[0].UUID != "thread-1" || threads[1].UUID != "thread-3" {
		t.Fatalf("threads = %#v, want successful threads in reference order", threads)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %#v, want one failure", failures)
	}
	if failures[0].UUID != "thread-2" || failures[0].Title != "Second" || failures[0].Stage != models.ThreadExportStageFetch {
		t.Errorf("failure = %#v, want thread-2 fetch failure", failures[0])
	}
}

func TestFetchThreadDetailsTracksFinalWriteFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "threads"), []byte("not a directory"), 0600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	jsonExp := &export.JSONExporter{OutputDir: dir}
	refs := []models.ThreadRef{{UUID: "thread-1", Title: "First"}}
	fetch := func(_ context.Context, uuid string, _ *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
		return &models.Thread{UUID: uuid, Slug: uuid, Complete: true}, nil
	}

	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("got %d successful threads, want none", len(threads))
	}
	if len(failures) != 1 || failures[0].UUID != "thread-1" || failures[0].Stage != models.ThreadExportStageWrite {
		t.Fatalf("failures = %#v, want thread-1 write failure", failures)
	}
}

func TestFetchThreadDetailsTracksCheckpointFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "threads"), []byte("not a directory"), 0600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	jsonExp := &export.JSONExporter{OutputDir: dir}
	refs := []models.ThreadRef{{UUID: "thread-1", Title: "First"}}
	fetch := func(_ context.Context, uuid string, _ *models.Thread, onPage func(*models.Thread) error) (*models.Thread, error) {
		partial := &models.Thread{UUID: uuid, Slug: uuid, NextCursor: "next"}
		if err := onPage(partial); err != nil {
			return nil, fmt.Errorf("checkpoint failed: %w", err)
		}
		return nil, errors.New("checkpoint unexpectedly succeeded")
	}

	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("got %d successful threads, want none", len(threads))
	}
	if len(failures) != 1 || failures[0].UUID != "thread-1" || failures[0].Stage != models.ThreadExportStageFetch {
		t.Fatalf("failures = %#v, want checkpoint failure recorded for thread-1", failures)
	}
}

func TestFetchThreadDetailsFailedRefreshDoesNotUseStaleCache(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	oldUpdatedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	stale := &models.Thread{UUID: "thread-1", Slug: "thread-1", UpdatedAt: oldUpdatedAt, Complete: true}
	if err := jsonExp.ExportThread(stale); err != nil {
		t.Fatalf("seed stale thread: %v", err)
	}
	refs := []models.ThreadRef{{
		UUID:              "thread-1",
		Title:             "First",
		UpdatedAt:         oldUpdatedAt.Add(time.Hour),
		PreviousUpdatedAt: oldUpdatedAt,
	}}
	fetch := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		return nil, errors.New("refresh failed")
	}

	cmd := &ExportCmd{Refresh: true}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("stale cached thread counted as successful: %#v", threads)
	}
	if len(failures) != 1 || failures[0].UUID != "thread-1" {
		t.Fatalf("failures = %#v, want failed refresh", failures)
	}
}

func TestFetchThreadDetailsCancellationTakesPrecedence(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	refs := []models.ThreadRef{{UUID: "thread-1"}, {UUID: "thread-2"}}
	fetch := func(_ context.Context, uuid string, _ *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
		if uuid == "thread-1" {
			return nil, errors.New("ordinary failure")
		}
		return nil, fmt.Errorf("fetch stopped: %w", context.Canceled)
	}

	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if threads != nil {
		t.Fatalf("threads=%#v, want cancellation to abort successful result", threads)
	}
	if len(failures) != 1 || failures[0].UUID != "thread-1" {
		t.Fatalf("failures=%#v, want prior failure preserved for retry state", failures)
	}
}

func TestNeedsThreadFetchHonorsPersistedRetry(t *testing.T) {
	cached := &models.Thread{Complete: true}
	ref := models.ThreadRef{UUID: "thread-1", RetryRequired: true}
	if !needsThreadFetch(false, ref, cached, nil) {
		t.Fatal("needsThreadFetch = false, want persisted retry to force fetch")
	}
}

func TestPersistThreadRetryStateRoundTrips(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	index := &models.ThreadIndex{
		Threads:  []models.ThreadRef{{UUID: "thread-1"}, {UUID: "thread-2", RetryRequired: true}},
		Total:    2,
		Complete: true,
	}
	if err := jsonExp.SaveThreadIndex(index); err != nil {
		t.Fatalf("SaveThreadIndex: %v", err)
	}
	refs := []models.ThreadRef{{UUID: "thread-1", RetryRequired: true}, {UUID: "thread-2"}}
	if err := persistThreadRetryState(jsonExp, refs); err != nil {
		t.Fatalf("persistThreadRetryState: %v", err)
	}

	reloaded, err := jsonExp.LoadThreadIndex()
	if err != nil {
		t.Fatalf("LoadThreadIndex: %v", err)
	}
	if !reloaded.Threads[0].RetryRequired || reloaded.Threads[1].RetryRequired {
		t.Fatalf("retry state = %#v, want thread-1 only", reloaded.Threads)
	}
}

func TestFetchThreadDetailsPreCancelledContext(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	fetch := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		called = true
		return nil, nil
	}
	cmd := &ExportCmd{}
	_, _, err := cmd.fetchThreadDetails(ctx, jsonExp, []models.ThreadRef{{UUID: "thread-1"}}, fetch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("fetch called for pre-cancelled context")
	}
}

func TestFetchThreadDetailsCancellationPreservesPendingRetryState(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	refs := []models.ThreadRef{{UUID: "thread-1"}, {UUID: "thread-2"}}
	fetch := func(_ context.Context, uuid string, _ *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
		cancel()
		return nil, fmt.Errorf("cancel %s: %w", uuid, context.Canceled)
	}

	cmd := &ExportCmd{}
	_, _, err := cmd.fetchThreadDetails(ctx, jsonExp, refs, fetch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if !refs[0].RetryRequired || !refs[1].RetryRequired {
		t.Fatalf("retry state = %#v, want current and pending threads marked", refs)
	}
}

func TestBuildManifestReportsThreadCompleteness(t *testing.T) {
	threads := []models.Thread{
		{
			UUID:     "thread-1",
			Slug:     "first",
			Complete: true,
			Entries:  []models.Entry{{Sources: []models.Source{{URL: "https://example.com"}}}},
		},
	}
	failures := []models.ThreadExportFailure{{UUID: "thread-2", Stage: models.ThreadExportStageFetch, Error: "failed"}}
	account := &models.Account{GlobalSkills: []models.Skill{{ID: "skill-1"}}}

	manifest := buildManifest([]string{"json"}, threads, []models.Space{{UUID: "space-1"}}, account, 2, failures)
	if manifest.ThreadsComplete {
		t.Fatal("ThreadsComplete = true, want false")
	}
	if manifest.ExpectedThreads != 2 || manifest.Counts.Threads != 1 {
		t.Errorf("expected=%d exported=%d, want 2 and 1", manifest.ExpectedThreads, manifest.Counts.Threads)
	}
	if manifest.Counts.Spaces != 1 || manifest.Counts.Sources != 1 || manifest.Counts.GlobalSkills != 1 {
		t.Errorf("counts = %#v, want one space, source, and global skill", manifest.Counts)
	}
	if len(manifest.FailedThreads) != 1 || manifest.FailedThreads[0].UUID != "thread-2" {
		t.Errorf("FailedThreads = %#v, want thread-2", manifest.FailedThreads)
	}
	if manifest.ThreadIndex["thread-1"] != "first" {
		t.Errorf("ThreadIndex = %#v, want exported thread", manifest.ThreadIndex)
	}

	complete := buildManifest([]string{"json"}, nil, nil, nil, 0, nil)
	if !complete.ThreadsComplete || complete.ExpectedThreads != 0 {
		t.Errorf("no-thread manifest = %#v, want complete with zero expected threads", complete)
	}
}

func TestFinalizeExportWritesPartialManifestBeforeReturningError(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	manifest := &models.ExportManifest{
		ThreadsComplete: false,
		ExpectedThreads: 2,
		FailedThreads: []models.ThreadExportFailure{{
			UUID:  "thread-2",
			Stage: models.ThreadExportStageFetch,
			Error: "failed",
		}},
	}

	err := finalizeExport(jsonExp, manifest)
	if !errors.Is(err, errIncompleteThreadExport) {
		t.Fatalf("error = %v, want errIncompleteThreadExport", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(jsonExp.OutputDir, "manifest.json"))
	if readErr != nil {
		t.Fatalf("manifest was not written before error: %v", readErr)
	}
	if !strings.Contains(string(raw), `"threads_complete": false`) || !strings.Contains(string(raw), `"uuid": "thread-2"`) {
		t.Fatalf("manifest = %s, want partial status and failed thread", raw)
	}
}
