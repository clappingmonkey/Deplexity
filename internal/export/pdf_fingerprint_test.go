package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func fingerprintTestThread() models.Thread {
	return models.Thread{
		UUID:      "thread-a",
		Slug:      "thread-a",
		Title:     "Title",
		CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Entries: []models.Entry{{
			UUID:   "entry-1",
			Query:  "Question",
			Answer: "Answer",
			Model:  "model-a",
			Sources: []models.Source{{
				Title:   "Source",
				URL:     "https://www.example.com/path?X-Amz-Signature=abc",
				Snippet: "snippet",
				Favicon: "https://example.com/favicon.ico",
			}},
		}},
	}
}

func mustFingerprint(t *testing.T, thread models.Thread) string {
	t.Helper()
	fingerprint, err := ThreadPDFFingerprint(&thread)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestThreadPDFFingerprintIsStableAndVersioned(t *testing.T) {
	first := mustFingerprint(t, fingerprintTestThread())
	second := mustFingerprint(t, fingerprintTestThread())
	if first != second {
		t.Fatalf("fingerprint not stable: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "v1:sha256:") || len(first) != len("v1:sha256:")+64 {
		t.Fatalf("fingerprint %q does not have the v1:sha256:<hex> form", first)
	}
}

func TestThreadPDFFingerprintTracksRenderedFields(t *testing.T) {
	base := mustFingerprint(t, fingerprintTestThread())
	tests := []struct {
		name   string
		mutate func(*models.Thread)
	}{
		{"title", func(th *models.Thread) { th.Title = "Other" }},
		{"empty title", func(th *models.Thread) { th.Title = "" }},
		{"created date", func(th *models.Thread) { th.CreatedAt = th.CreatedAt.AddDate(0, 0, 1) }},
		{"query", func(th *models.Thread) { th.Entries[0].Query = "Other" }},
		{"answer", func(th *models.Thread) { th.Entries[0].Answer = "Other" }},
		{"model", func(th *models.Thread) { th.Entries[0].Model = "model-b" }},
		{"source title", func(th *models.Thread) { th.Entries[0].Sources[0].Title = "Other" }},
		{"source domain", func(th *models.Thread) { th.Entries[0].Sources[0].URL = "https://other.example/path" }},
		{"added source", func(th *models.Thread) {
			th.Entries[0].Sources = append(th.Entries[0].Sources, models.Source{URL: "https://b.example"})
		}},
		{"added entry", func(th *models.Thread) { th.Entries = append(th.Entries, models.Entry{Query: "Next"}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thread := fingerprintTestThread()
			tt.mutate(&thread)
			if got := mustFingerprint(t, thread); got == base {
				t.Fatalf("fingerprint unchanged after %s change", tt.name)
			}
		})
	}
}

func TestThreadPDFFingerprintIgnoresUnrenderedFields(t *testing.T) {
	base := mustFingerprint(t, fingerprintTestThread())
	tests := []struct {
		name   string
		mutate func(*models.Thread)
	}{
		{"updated at", func(th *models.Thread) { th.UpdatedAt = th.UpdatedAt.Add(time.Hour) }},
		{"created time of day", func(th *models.Thread) { th.CreatedAt = th.CreatedAt.Add(time.Hour) }},
		{"bookmarked", func(th *models.Thread) { th.Bookmarked = true }},
		{"entry uuid", func(th *models.Thread) { th.Entries[0].UUID = "entry-2" }},
		{"source snippet", func(th *models.Thread) { th.Entries[0].Sources[0].Snippet = "other" }},
		{"source favicon", func(th *models.Thread) { th.Entries[0].Sources[0].Favicon = "" }},
		{"source url path", func(th *models.Thread) {
			th.Entries[0].Sources[0].URL = "https://www.example.com/other?X-Amz-Signature=def"
		}},
		{"source url www prefix", func(th *models.Thread) {
			th.Entries[0].Sources[0].URL = "https://example.com/path?X-Amz-Signature=abc"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thread := fingerprintTestThread()
			tt.mutate(&thread)
			if got := mustFingerprint(t, thread); got != base {
				t.Fatalf("fingerprint changed after %s change", tt.name)
			}
		})
	}
}

func TestPDFUpToDate(t *testing.T) {
	thread := fingerprintTestThread()
	fingerprint := mustFingerprint(t, thread)

	tests := []struct {
		name        string
		pdf         *string
		fingerprint *string
		want        bool
	}{
		{name: "matching", pdf: ptr("pdf"), fingerprint: ptr(fingerprint + "\n"), want: true},
		{name: "missing pdf", fingerprint: ptr(fingerprint + "\n")},
		{name: "empty pdf", pdf: ptr(""), fingerprint: ptr(fingerprint + "\n")},
		{name: "missing fingerprint", pdf: ptr("pdf")},
		{name: "different fingerprint", pdf: ptr("pdf"), fingerprint: ptr("v1:sha256:" + strings.Repeat("0", 64))},
		{name: "old format", pdf: ptr("pdf"), fingerprint: ptr("v0:" + fingerprint)},
		{name: "oversized fingerprint", pdf: ptr("pdf"), fingerprint: ptr(fingerprint + strings.Repeat(" ", maxPDFFingerprintSize))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := NewPDFExporter(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(exporter.ThreadPDFPath(&thread)), 0755); err != nil {
				t.Fatal(err)
			}
			if tt.pdf != nil {
				if err := os.WriteFile(exporter.ThreadPDFPath(&thread), []byte(*tt.pdf), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.fingerprint != nil {
				if err := os.WriteFile(exporter.ThreadPDFFingerprintPath(&thread), []byte(*tt.fingerprint), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if got := exporter.PDFUpToDate(&thread, fingerprint); got != tt.want {
				t.Fatalf("PDFUpToDate = %t, want %t", got, tt.want)
			}
		})
	}

	t.Run("symlinked pdf", func(t *testing.T) {
		dir := t.TempDir()
		exporter := NewPDFExporter(dir)
		pdfPath := exporter.ThreadPDFPath(&thread)
		if err := os.MkdirAll(filepath.Dir(pdfPath), 0755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "elsewhere.pdf")
		if err := os.WriteFile(target, []byte("pdf"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, pdfPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.WriteFile(exporter.ThreadPDFFingerprintPath(&thread), []byte(fingerprint+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if exporter.PDFUpToDate(&thread, fingerprint) {
			t.Fatal("PDFUpToDate accepted a symlinked PDF")
		}
	})
}

func TestPDFFingerprintWriteAndRemove(t *testing.T) {
	thread := fingerprintTestThread()
	exporter := NewPDFExporter(t.TempDir())
	if err := exporter.RemovePDFFingerprint(&thread); err != nil {
		t.Fatalf("RemovePDFFingerprint on missing file: %v", err)
	}
	if err := exporter.ExportThread(&thread); err != nil {
		t.Fatal(err)
	}
	fingerprint := mustFingerprint(t, thread)
	if !exporter.PDFUpToDate(&thread, fingerprint) {
		t.Fatal("ExportThread did not record a matching fingerprint")
	}
	if err := exporter.RemovePDFFingerprint(&thread); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exporter.ThreadPDFFingerprintPath(&thread)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fingerprint still exists after removal: %v", err)
	}
	if exporter.PDFUpToDate(&thread, fingerprint) {
		t.Fatal("PDF reported up to date without a fingerprint")
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(exporter.ThreadPDFFingerprintPath(&thread)), pdfFingerprintFile+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary fingerprint files remain: %v", matches)
	}
}

func TestPDFExportSpacesDoesNotCopyFingerprint(t *testing.T) {
	dir := t.TempDir()
	exporter := NewPDFExporter(dir)
	thread := fingerprintTestThread()
	if err := exporter.ExportThread(&thread); err != nil {
		t.Fatal(err)
	}
	spaces := []models.Space{{UUID: "space", Name: "Space", ThreadUUIDs: []string{thread.UUID}}}
	if err := exporter.ExportSpaces(context.Background(), spaces, []models.Thread{thread}); err != nil {
		t.Fatal(err)
	}
	spaceThreadDir := filepath.Join(dir, "spaces", spaceDirNames(spaces)[0], "threads", threadDirName(thread.Slug, thread.UUID))
	if _, err := os.Stat(filepath.Join(spaceThreadDir, "thread.pdf")); err != nil {
		t.Fatalf("space PDF missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spaceThreadDir, pdfFingerprintFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fingerprint copied into space folder: %v", err)
	}
}

func ptr(s string) *string { return &s }
