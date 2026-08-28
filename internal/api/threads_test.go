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

func cursorPtr(s string) *string { return &s }

func TestThreadPagePathEscapesCursor(t *testing.T) {
	cursor := `{"M":{"entry_uuid":{"S":"abc"}}}`
	path := threadPagePath("thread-1", cursor)

	if !strings.Contains(path, "offset=0") {
		t.Errorf("path = %q, want offset pinned to 0", path)
	}
	if !strings.Contains(path, "limit=100") {
		t.Errorf("path = %q, want limit=100", path)
	}
	if !strings.Contains(path, "cursor=%7B%22M%22") {
		t.Errorf("path = %q, want URL-escaped cursor", path)
	}
	if strings.Contains(path, cursor) {
		t.Errorf("path = %q, want the raw cursor JSON escaped", path)
	}
}

func TestThreadPagePathOmitsEmptyCursor(t *testing.T) {
	if path := threadPagePath("thread-1", ""); strings.Contains(path, "cursor=") {
		t.Errorf("path = %q, want no cursor param on the first page", path)
	}
}

func TestGetThreadPaginatesByCursor(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{
		{
			ThreadMetadata: ThreadMetadata{Title: "Long thread"},
			Entries:        []ThreadEntry{{UUID: "entry-1"}, {UUID: "entry-2"}},
			HasNextPage:    true,
			NextCursor:     cursorPtr("cursor-1"),
		},
		{
			// A cursor boundary re-includes the last entry of the prior page.
			Entries:     []ThreadEntry{{UUID: "entry-2"}, {UUID: "entry-3"}},
			HasNextPage: false,
			NextCursor:  cursorPtr(""),
		},
	}}

	thread, err := GetThread(context.Background(), getter, "thread-1", nil, nil)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if getter.calls != 2 {
		t.Errorf("made %d requests, want 2", getter.calls)
	}
	if strings.Contains(getter.paths[0], "cursor=") {
		t.Errorf("first request = %q, want no cursor", getter.paths[0])
	}
	if !strings.Contains(getter.paths[1], "cursor=cursor-1") {
		t.Errorf("second request = %q, want cursor from first page", getter.paths[1])
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
	if thread.NextCursor != "" {
		t.Errorf("NextCursor = %q, want cleared on completion", thread.NextCursor)
	}
}

func TestGetThreadStopsWhenCursorRepeats(t *testing.T) {
	// A reverse-engineered endpoint could return the same cursor forever;
	// the fetch must terminate with an error rather than spinning, and must
	// not mark the partially-fetched thread complete.
	repeating := ThreadDetailResponse{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}},
		HasNextPage:    true,
		NextCursor:     cursorPtr("cursor-1"),
	}
	responses := make([]ThreadDetailResponse, 10)
	for i := range responses {
		responses[i] = repeating
	}
	getter := &threadGetter{responses: responses}

	thread, err := GetThread(context.Background(), getter, "thread-1", nil, nil)
	if err == nil {
		t.Fatal("GetThread succeeded despite a non-advancing cursor")
	}
	if thread != nil {
		t.Fatalf("GetThread returned a thread: %+v", thread)
	}
	if !strings.Contains(err.Error(), "cursor") {
		t.Errorf("error = %q, want cursor-loop context", err)
	}
	// Page 1 (no cursor) + page 2 (cursor-1) is enough to detect the repeat.
	if getter.calls > 2 {
		t.Errorf("made %d requests, want stop after detecting the repeat", getter.calls)
	}
}

func TestGetThreadReturnsErrorWhenLaterPageFails(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}},
		HasNextPage:    true,
		NextCursor:     cursorPtr("cursor-1"),
	}}}

	thread, err := GetThread(context.Background(), getter, "thread-1", nil, nil)
	if err == nil {
		t.Fatal("GetThread succeeded after a later page failed")
	}
	if thread != nil {
		t.Fatalf("GetThread returned a partial thread: %+v", thread)
	}
	if !strings.Contains(err.Error(), "thread-1") {
		t.Errorf("error = %q, want thread context", err)
	}
}

func TestGetThreadCheckpointsProgressBeforeFailure(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}, {UUID: "entry-2"}},
		HasNextPage:    true,
		NextCursor:     cursorPtr("cursor-1"),
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
	if cp.NextCursor != "cursor-1" {
		t.Errorf("checkpoint NextCursor = %q, want cursor-1", cp.NextCursor)
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
		// The cursor boundary re-serves entry-2.
		Entries:     []ThreadEntry{{UUID: "entry-2"}, {UUID: "entry-3"}},
		HasNextPage: false,
		NextCursor:  cursorPtr(""),
	}}}

	resume := &models.Thread{
		UUID:       "thread-1",
		Title:      "Long thread",
		Entries:    []models.Entry{{UUID: "entry-1"}, {UUID: "entry-2"}},
		NextCursor: "cursor-1",
	}

	thread, err := GetThread(context.Background(), getter, "thread-1", resume, nil)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if getter.calls != 1 {
		t.Errorf("made %d requests, want 1 (no refetch from the start)", getter.calls)
	}
	if !strings.Contains(getter.paths[0], "cursor=cursor-1") {
		t.Errorf("first request = %q, want resume at cursor-1", getter.paths[0])
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
	if thread.NextCursor != "" {
		t.Errorf("NextCursor = %q, want cleared on completion", thread.NextCursor)
	}
}

func TestGetThreadHonorsCancellation(t *testing.T) {
	getter := &threadGetter{responses: []ThreadDetailResponse{{
		ThreadMetadata: ThreadMetadata{Title: "Long thread"},
		Entries:        []ThreadEntry{{UUID: "entry-1"}},
		HasNextPage:    true,
		NextCursor:     cursorPtr("cursor-1"),
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
