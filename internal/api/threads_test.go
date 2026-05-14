package api

import (
	"testing"
	"time"
)

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

func TestThreadSummaryMapping(t *testing.T) {
	raw := ThreadSummary{
		UUID:       "abc-123",
		Slug:       "test-slug",
		Title:      "Test Thread",
		CreatedAt:  "2024-01-01T00:00:00Z",
		UpdatedAt:  "2024-01-02T00:00:00Z",
		SpaceUUID:  "space-1",
		Bookmarked: true,
	}

	if raw.UUID != "abc-123" {
		t.Errorf("unexpected UUID: %s", raw.UUID)
	}
	if raw.Bookmarked != true {
		t.Error("expected bookmarked to be true")
	}
}
