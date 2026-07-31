package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

type threadGetter struct {
	responses []ThreadDetailResponse
	calls     int
	paths     []string
}

func (g *threadGetter) Get(_ context.Context, path string, dst any) error {
	g.paths = append(g.paths, path)
	if g.calls >= len(g.responses) {
		return errors.New("rate limited")
	}
	*dst.(*ThreadDetailResponse) = g.responses[g.calls]
	g.calls++
	return nil
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			name:  "RFC3339",
			input: "2024-06-15T10:30:00Z",
			want:  time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			name:  "RFC3339Nano",
			input: "2024-06-15T10:30:00.123456789Z",
			want:  time.Date(2024, 6, 15, 10, 30, 0, 123456789, time.UTC),
		},
		{
			name:  "with milliseconds",
			input: "2024-06-15T10:30:00.000Z",
			want:  time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			name:  "space separated",
			input: "2024-06-15 10:30:00",
			want:  time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			name:  "empty string",
			input: "",
			want:  time.Time{},
		},
		{
			name:  "garbage",
			input: "not-a-date",
			want:  time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTime(tt.input)
			if !got.Equal(tt.want) {
				t.Errorf("parseTime(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestThreadListItemMapping(t *testing.T) {
	raw := ThreadListItem{
		UUID:   "abc-123",
		Title:  "Test Thread",
		Slug:   "abc-123",
		Status: "completed",
	}

	if raw.UUID != "abc-123" {
		t.Errorf("unexpected UUID: %s", raw.UUID)
	}
	if raw.Title != "Test Thread" {
		t.Errorf("unexpected Title: %s", raw.Title)
	}
}

func TestGetThreadReturnsErrorWhenLaterPageFails(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}},
		HasNextPage:    true,
	}}}

	thread, err := GetThread(context.Background(), getter, "thread-1", nil, nil)
	if err == nil {
		t.Fatal("GetThread succeeded after a later page failed")
	}
	if thread != nil {
		t.Fatalf("GetThread returned a partial thread: %+v", thread)
	}
	if !strings.Contains(err.Error(), "offset 1") {
		t.Errorf("error = %q, want offset context", err)
	}
}

func TestGetThreadCheckpointsProgressBeforeFailure(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}, {UUID: "entry-2"}},
		HasNextPage:    true,
	}}}

	var checkpoints []models.Thread
	_, err := GetThread(context.Background(), getter, "thread-1", nil, func(t *models.Thread) error {
		checkpoints = append(checkpoints, *t)
		return nil
	})
	if err == nil {
		t.Fatal("GetThread succeeded after a later page failed")
	}
	if len(checkpoints) != 1 {
		t.Fatalf("got %d checkpoints, want 1", len(checkpoints))
	}
	cp := checkpoints[0]
	if cp.Complete {
		t.Error("checkpoint marked complete before the fetch finished")
	}
	if cp.NextOffset != 2 {
		t.Errorf("checkpoint NextOffset = %d, want 2", cp.NextOffset)
	}
	if len(cp.Entries) != 2 {
		t.Errorf("checkpoint has %d entries, want 2", len(cp.Entries))
	}
	if cp.Title != "Long thread" {
		t.Errorf("checkpoint Title = %q, want metadata preserved", cp.Title)
	}
}

func TestGetThreadResumesFromCheckpoint(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		// The API recycles results, so entry-2 is served again.
		Entries:     []ThreadEntry{{UUID: "entry-2"}, {UUID: "entry-3"}},
		HasNextPage: false,
	}}}

	resume := &models.Thread{
		UUID:       "thread-1",
		Title:      "Long thread",
		Entries:    []models.Entry{{UUID: "entry-1"}, {UUID: "entry-2"}},
		NextOffset: 2,
	}

	thread, err := GetThread(context.Background(), getter, "thread-1", resume, nil)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if getter.calls != 1 {
		t.Errorf("made %d requests, want 1 (no refetch from offset 0)", getter.calls)
	}
	if !strings.Contains(getter.paths[0], "offset=2") {
		t.Errorf("first request = %q, want resume at offset=2", getter.paths[0])
	}

	want := []string{"entry-1", "entry-2", "entry-3"}
	if len(thread.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(thread.Entries), len(want), thread.Entries)
	}
	for i, uuid := range want {
		if thread.Entries[i].UUID != uuid {
			t.Errorf("entry %d = %q, want %q", i, thread.Entries[i].UUID, uuid)
		}
	}
	if !thread.Complete {
		t.Error("completed thread not marked complete")
	}
	if thread.NextOffset != 0 {
		t.Errorf("NextOffset = %d, want cleared on completion", thread.NextOffset)
	}
}

func TestGetThreadStopsWhenPageHasOnlyRecycledEntries(t *testing.T) {
	// The API can keep reporting HasNextPage while serving only entries
	// already seen; the fetch must terminate instead of spinning.
	recycled := ThreadDetailResponse{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}},
		HasNextPage:    true,
	}
	responses := make([]ThreadDetailResponse, 10)
	for i := range responses {
		responses[i] = recycled
	}
	getter := &threadGetter{responses: responses}

	thread, err := GetThread(context.Background(), getter, "thread-1", nil, nil)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(thread.Entries) != 1 {
		t.Errorf("got %d entries, want 1 (duplicates dropped)", len(thread.Entries))
	}
	if !thread.Complete {
		t.Error("thread not marked complete")
	}
	if getter.calls > maxStalePages+1 {
		t.Errorf("made %d requests, want stop after %d stale pages", getter.calls, maxStalePages)
	}
}

func TestGetThreadContinuesPastOverlappingPage(t *testing.T) {
	// Pages can overlap, so a page carrying nothing new must not be mistaken
	// for the end of the thread while later pages still hold new entries.
	getter := &threadGetter{responses: []ThreadDetailResponse{
		{
			ThreadMetadata: ThreadMetadata{Title: "Long thread"},
			Entries:        []ThreadEntry{{UUID: "entry-1"}, {UUID: "entry-2"}},
			HasNextPage:    true,
		},
		{
			Entries:     []ThreadEntry{{UUID: "entry-1"}, {UUID: "entry-2"}},
			HasNextPage: true,
		},
		{
			Entries:     []ThreadEntry{{UUID: "entry-3"}},
			HasNextPage: false,
		},
	}}

	thread, err := GetThread(context.Background(), getter, "thread-1", nil, nil)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}

	want := []string{"entry-1", "entry-2", "entry-3"}
	if len(thread.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(thread.Entries), len(want), thread.Entries)
	}
	for i, uuid := range want {
		if thread.Entries[i].UUID != uuid {
			t.Errorf("entry %d = %q, want %q", i, thread.Entries[i].UUID, uuid)
		}
	}
	if !thread.Complete {
		t.Error("thread not marked complete")
	}
}

func TestGetThreadRestartsWhenResumeCheckpointIsStale(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{
		// Stale offset is past the end of the thread.
		{},
		{
			ThreadMetadata: ThreadMetadata{Title: "Rebuilt thread"},
			Entries:        []ThreadEntry{{UUID: "entry-1"}, {UUID: "entry-2"}},
			HasNextPage:    false,
		},
	}}

	resume := &models.Thread{
		UUID:       "thread-1",
		Title:      "Stale thread",
		Entries:    []models.Entry{{UUID: "ghost-1"}},
		NextOffset: 9000,
	}

	thread, err := GetThread(context.Background(), getter, "thread-1", resume, nil)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if !strings.Contains(getter.paths[1], "offset=0") {
		t.Errorf("second request = %q, want restart at offset=0", getter.paths[1])
	}
	if len(thread.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 rebuilt from scratch: %+v", len(thread.Entries), thread.Entries)
	}
	for _, e := range thread.Entries {
		if e.UUID == "ghost-1" {
			t.Error("stale checkpoint entry retained after restart")
		}
	}
	if thread.Title != "Rebuilt thread" {
		t.Errorf("Title = %q, want metadata from the restarted fetch", thread.Title)
	}
	if !thread.Complete {
		t.Error("thread not marked complete")
	}
}

func TestGetThreadHonorsCancellation(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}},
		HasNextPage:    true,
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := GetThread(ctx, getter, "thread-1", nil, func(*models.Thread) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
