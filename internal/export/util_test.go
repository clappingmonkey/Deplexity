package export

import "testing"

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World!", "hello-world"},
		{"test/file:name", "test-file-name"},
		{"  spaces  ", "spaces"},
		{"already-safe", "already-safe"},
		{"UPPERCASE", "uppercase"},
		{"", "unnamed"},
		{"a---b---c", "a-b-c"},
		{string(make([]byte, 200)), "unnamed"}, // all zeros → all replaced → collapsed
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilenameLongInput(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := sanitizeFilename(long)
	if len(got) > 128 {
		t.Errorf("expected length <= 128, got %d", len(got))
	}
}
