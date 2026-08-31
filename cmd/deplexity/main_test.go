package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
