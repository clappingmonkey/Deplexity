package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type threadGetter struct {
	responses []ThreadDetailResponse
	calls     int
}

func (g *threadGetter) Get(_ context.Context, _ string, dst any) error {
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

	thread, err := GetThread(context.Background(), getter, "thread-1")
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
