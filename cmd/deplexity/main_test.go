package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/kong"

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

func TestRequestDelayValidation(t *testing.T) {
	tests := []struct {
		name string
		ms   int
		want time.Duration
		err  string
	}{
		{name: "zero", ms: 0},
		{name: "positive", ms: 500, want: 500 * time.Millisecond},
		{name: "negative", ms: -1, err: "--delay must be non-negative"},
	}
	maxInt := int(^uint(0) >> 1)
	maxMilliseconds := int64((time.Duration(1<<63 - 1)) / time.Millisecond)
	if int64(maxInt) > maxMilliseconds {
		tests = append(tests, struct {
			name string
			ms   int
			want time.Duration
			err  string
		}{name: "overflow", ms: int(maxMilliseconds + 1), err: "--delay is too large"})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := requestDelay(tt.ms)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("requestDelay(%d) error = %v, want %q", tt.ms, err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("requestDelay(%d): %v", tt.ms, err)
			}
			if got != tt.want {
				t.Errorf("requestDelay(%d) = %v, want %v", tt.ms, got, tt.want)
			}
		})
	}
}

func TestExportRejectsNegativeDelayBeforeSessionLoad(t *testing.T) {
	cmd := &ExportCmd{Delay: -1}
	if err := cmd.Run(context.Background()); err == nil || err.Error() != "--delay must be non-negative" {
		t.Fatalf("Run error = %v, want negative delay validation", err)
	}
}

func TestExportRejectsNegativePDFTimeoutBeforeSessionLoad(t *testing.T) {
	cmd := &ExportCmd{PDFTimeout: -time.Second}
	err := cmd.Run(context.Background())
	if err == nil || err.Error() != "--pdf-timeout must be non-negative" {
		t.Fatalf("Run error = %v, want negative timeout validation", err)
	}
}

func TestPDFTimeoutDefault(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"export"}); err != nil {
		t.Fatal(err)
	}
	if cli.Export.PDFTimeout != 30*time.Minute {
		t.Fatalf("PDFTimeout = %v, want 30m", cli.Export.PDFTimeout)
	}
}

func TestRenderPDFIsolatedTimeoutIsExplicitAndPreservesExistingPDF(t *testing.T) {
	restore := usePDFTestProcess(t, "block", "")
	defer restore()

	dir := t.TempDir()
	target := filepath.Join(dir, "thread.pdf")
	if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	thread := &models.Thread{UUID: "thread-123", Title: "Slow report"}
	err := renderPDFIsolated(context.Background(), 50*time.Millisecond, thread, target)
	if err == nil {
		t.Fatal("expected PDF timeout")
	}
	for _, want := range []string{"PDF TIMEOUT", `thread "Slow report" (thread-123)`, "50ms per-thread limit", "renderer was terminated", "no new PDF was published", "existing PDF was left unchanged", "remaining PDF work was canceled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout error %q does not contain %q", err, want)
		}
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "existing" {
		t.Fatalf("existing PDF = %q, want unchanged", data)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".thread.pdf.render-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("staging files remain after timeout: %v", matches)
	}
}

func TestRenderPDFIsolatedCancellationTerminatesRenderer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	restore := usePDFTestProcess(t, "block", marker)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	thread := &models.Thread{UUID: "thread-123", Title: "Canceled report"}
	result := make(chan error, 1)
	go func() {
		result <- renderPDFIsolated(ctx, 30*time.Minute, thread, filepath.Join(t.TempDir(), "thread.pdf"))
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("renderer process did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("renderPDFIsolated error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("canceled renderer returned after %v", elapsed)
	}
}

func TestRenderPDFIsolatedPublishesCompletedPDF(t *testing.T) {
	restore := usePDFTestProcess(t, "write", "")
	defer restore()

	target := filepath.Join(t.TempDir(), "thread.pdf")
	thread := &models.Thread{UUID: "thread-123", Title: "Completed report"}
	if err := renderPDFIsolated(context.Background(), 0, thread, target); err != nil {
		t.Fatalf("renderPDFIsolated: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rendered:thread-123" {
		t.Fatalf("published PDF = %q, want rendered:thread-123", data)
	}
}

func TestRenderPDFIsolatedRejectsEmptyOutput(t *testing.T) {
	restore := usePDFTestProcess(t, "empty", "")
	defer restore()

	target := filepath.Join(t.TempDir(), "thread.pdf")
	thread := &models.Thread{UUID: "thread-123", Title: "Empty report"}
	err := renderPDFIsolated(context.Background(), time.Minute, thread, target)
	if err == nil || !strings.Contains(err.Error(), "produced an empty file") {
		t.Fatalf("renderPDFIsolated error = %v, want empty output error", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target PDF exists after empty output: %v", err)
	}
}

func TestRenderPDFIsolatedZeroTimeoutHasNoInternalDeadline(t *testing.T) {
	previous := pdfCommand
	defer func() { pdfCommand = previous }()
	pdfCommand = func(ctx context.Context) (*exec.Cmd, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("zero PDF timeout added an internal deadline")
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestPDFRendererProcess")
		cmd.Env = append(os.Environ(), "DEPLEXITY_PDF_TEST_PROCESS=1", "DEPLEXITY_PDF_TEST_MODE=write")
		return cmd, nil
	}
	thread := &models.Thread{UUID: "thread-123", Title: "No timeout"}
	if err := renderPDFIsolated(context.Background(), 0, thread, filepath.Join(t.TempDir(), "thread.pdf")); err != nil {
		t.Fatal(err)
	}
}

func TestRunPDFRenderHelper(t *testing.T) {
	request, err := json.Marshal(pdfRenderRequest{Thread: models.Thread{UUID: "thread-123", Title: "Helper test"}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runPDFRenderHelper(bytes.NewReader(request), &output); err != nil {
		t.Fatalf("runPDFRenderHelper: %v", err)
	}
	if output.Len() == 0 {
		t.Fatal("helper produced an empty PDF")
	}
}

func TestRunPDFRenderHelperRejectsInvalidInput(t *testing.T) {
	if err := runPDFRenderHelper(strings.NewReader("not-json"), io.Discard); err == nil || !strings.Contains(err.Error(), "could not decode PDF render request") {
		t.Fatalf("runPDFRenderHelper error = %v, want decode error", err)
	}
}

func TestRunPDFRenderHelperRejectsTrailingJSON(t *testing.T) {
	input := `{"thread":{"uuid":"thread-123"}} {"unexpected":true}`
	if err := runPDFRenderHelper(strings.NewReader(input), io.Discard); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("runPDFRenderHelper error = %v, want trailing JSON error", err)
	}
}

func TestLimitedBufferCapsChildDiagnostics(t *testing.T) {
	var buffer limitedBuffer
	payload := strings.Repeat("x", maxPDFErrorSize+100)
	if n, err := buffer.Write([]byte(payload)); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := buffer.String(); len(got) > maxPDFErrorSize+len("... [truncated]") || !strings.HasSuffix(got, "... [truncated]") {
		t.Fatalf("bounded diagnostics length/suffix = %d, %q", len(got), got[len(got)-20:])
	}
}

func TestRunPDFWorkersStopsQueuedWorkAfterFailure(t *testing.T) {
	threads := []models.Thread{{UUID: "first"}, {UUID: "second"}, {UUID: "queued"}}
	failure := errors.New("render failed")
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int64

	errCh := make(chan error, 1)
	go func() {
		errCh <- runPDFWorkers(context.Background(), threads, 2, func(ctx context.Context, thread *models.Thread) error {
			calls.Add(1)
			if thread.UUID == "first" {
				return failure
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		})
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, failure) {
			t.Fatalf("runPDFWorkers error = %v, want render failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("workers did not stop after first failure")
	}
	if got := calls.Load(); got > 2 {
		t.Fatalf("render calls = %d, want no queued work after failure", got)
	}
}

func TestRunPDFWorkersCancellationStartsNoWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int64
	err := runPDFWorkers(ctx, []models.Thread{{UUID: "thread"}}, 1, func(context.Context, *models.Thread) error {
		calls.Add(1)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runPDFWorkers error = %v, want context.Canceled", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("render calls = %d, want 0", calls.Load())
	}
}

func usePDFTestProcess(t *testing.T, mode, marker string) func() {
	t.Helper()
	previous := pdfCommand
	pdfCommand = func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestPDFRendererProcess")
		cmd.Env = append(os.Environ(), "DEPLEXITY_PDF_TEST_PROCESS=1", "DEPLEXITY_PDF_TEST_MODE="+mode, "DEPLEXITY_PDF_TEST_MARKER="+marker)
		return cmd, nil
	}
	return func() { pdfCommand = previous }
}

func TestPDFRendererProcess(t *testing.T) {
	if os.Getenv("DEPLEXITY_PDF_TEST_PROCESS") != "1" {
		return
	}
	var request pdfRenderRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	if marker := os.Getenv("DEPLEXITY_PDF_TEST_MARKER"); marker != "" {
		if err := os.WriteFile(marker, nil, 0600); err != nil {
			os.Exit(5)
		}
	}
	switch os.Getenv("DEPLEXITY_PDF_TEST_MODE") {
	case "block":
		for {
			time.Sleep(time.Hour)
		}
	case "write":
		if _, err := os.Stdout.Write([]byte("rendered:" + request.Thread.UUID)); err != nil {
			os.Exit(3)
		}
	case "empty":
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

// countPDFRenders stubs the PDF helper with the given test mode and counts how
// many times it is launched.
func countPDFRenders(t *testing.T, mode string) *atomic.Int64 {
	t.Helper()
	previous := pdfCommand
	var count atomic.Int64
	pdfCommand = func(ctx context.Context) (*exec.Cmd, error) {
		count.Add(1)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestPDFRendererProcess")
		cmd.Env = append(os.Environ(), "DEPLEXITY_PDF_TEST_PROCESS=1", "DEPLEXITY_PDF_TEST_MODE="+mode)
		return cmd, nil
	}
	t.Cleanup(func() { pdfCommand = previous })
	return &count
}

func pdfSkipTestThreads() []models.Thread {
	return []models.Thread{
		{UUID: "thread-a", Slug: "thread-a", Title: "A", Entries: []models.Entry{{Query: "qa", Answer: "aa"}}},
		{UUID: "thread-b", Slug: "thread-b", Title: "B", Entries: []models.Entry{{Query: "qb", Answer: "ab"}}},
	}
}

// assertPDFsUpToDate checks that every thread's canonical PDF matches its
// current fingerprint.
func assertPDFsUpToDate(t *testing.T, exp *export.PDFExporter, threads []models.Thread) {
	t.Helper()
	for i := range threads {
		fingerprint, err := export.ThreadPDFFingerprint(&threads[i])
		if err != nil {
			t.Fatal(err)
		}
		if !exp.PDFUpToDate(&threads[i], fingerprint) {
			t.Fatalf("thread %s not up to date after export", threads[i].UUID)
		}
	}
}

// TestExportPDFSkipsUpToDateThreads runs successive exports into one output
// directory. Before each later run every PDF is overwritten with a "stale"
// marker (fingerprints untouched), so a PDF still holding the marker afterwards
// was skipped and one holding "rendered:<uuid>" was re-rendered.
func TestExportPDFSkipsUpToDateThreads(t *testing.T) {
	dir := t.TempDir()
	threads := pdfSkipTestThreads()
	exp := export.NewPDFExporter(dir)

	runExport := func(force bool) {
		t.Helper()
		cmd := &ExportCmd{Output: dir, PDFTimeout: time.Minute, ForcePDF: force}
		if err := cmd.exportFormat(context.Background(), "pdf", threads, nil, nil, nil); err != nil {
			t.Fatalf("exportFormat: %v", err)
		}
		assertPDFsUpToDate(t, exp, threads)
	}
	markStale := func() {
		t.Helper()
		for i := range threads {
			if err := os.WriteFile(exp.ThreadPDFPath(&threads[i]), []byte("stale"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertRendered := func(step string, count *atomic.Int64, want ...string) {
		t.Helper()
		if got := count.Load(); got != int64(len(want)) {
			t.Fatalf("%s: PDF renders = %d, want %d", step, got, len(want))
		}
		for i := range threads {
			data, err := os.ReadFile(exp.ThreadPDFPath(&threads[i]))
			if err != nil {
				t.Fatal(err)
			}
			wantContent := "stale"
			if slices.Contains(want, threads[i].UUID) {
				wantContent = "rendered:" + threads[i].UUID
			}
			if string(data) != wantContent {
				t.Fatalf("%s: PDF for %s = %q, want %q", step, threads[i].UUID, data, wantContent)
			}
		}
	}

	count := countPDFRenders(t, "write")
	runExport(false)
	assertRendered("first run", count, "thread-a", "thread-b")

	markStale()
	count = countPDFRenders(t, "write")
	runExport(false)
	assertRendered("unchanged run", count)

	markStale()
	threads[1].Entries[0].Answer = "changed"
	count = countPDFRenders(t, "write")
	runExport(false)
	assertRendered("rendered change", count, "thread-b")

	markStale()
	threads[0].UpdatedAt = time.Now()
	threads[0].Entries[0].Sources = []models.Source{{Title: "Doc", URL: "https://example.com/a", Snippet: "s"}}
	count = countPDFRenders(t, "write")
	runExport(false)
	assertRendered("new source", count, "thread-a")

	markStale()
	threads[0].UpdatedAt = time.Now().Add(time.Hour)
	threads[0].Entries[0].Sources[0].URL = "https://www.example.com/b?sig=1"
	threads[0].Entries[0].Sources[0].Snippet = "changed snippet"
	count = countPDFRenders(t, "write")
	runExport(false)
	assertRendered("unrendered changes", count)

	markStale()
	count = countPDFRenders(t, "write")
	runExport(true)
	assertRendered("force", count, "thread-a", "thread-b")
}

func TestExportPDFAllUpToDateHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	threads := pdfSkipTestThreads()
	countPDFRenders(t, "write")
	cmd := &ExportCmd{Output: dir, PDFTimeout: time.Minute}
	if err := cmd.exportFormat(context.Background(), "pdf", threads, nil, nil, nil); err != nil {
		t.Fatalf("initial exportFormat: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "canceled", ctx: canceled, want: context.Canceled},
		{name: "deadline", ctx: expired, want: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			count := countPDFRenders(t, "write")
			if err := cmd.exportFormat(tt.ctx, "pdf", threads, nil, nil, nil); !errors.Is(err, tt.want) {
				t.Fatalf("exportFormat error = %v, want %v", err, tt.want)
			}
			if count.Load() != 0 {
				t.Fatalf("PDF renders = %d, want 0", count.Load())
			}
		})
	}
}

// TestPDFHelperRendersFingerprintedInput locks the assumption that the helper
// subprocess renders exactly what the parent fingerprinted: the thread survives
// the pdfRenderRequest JSON round trip with an identical fingerprint and PDF.
func TestPDFHelperRendersFingerprintedInput(t *testing.T) {
	thread := &models.Thread{
		UUID:      "thread-rt",
		Slug:      "round-trip",
		Title:     "Round trip <b>&</b> ünïcödé",
		CreatedAt: time.Date(2026, 3, 4, 23, 59, 59, 123456789, time.FixedZone("EET", 2*60*60)),
		UpdatedAt: time.Now(),
		Entries: []models.Entry{{
			Query:   "What?\nWhy?",
			Answer:  "**Because** `code` and a table:\n\n| a | b |\n|---|---|\n| 1 | 2 |",
			Model:   "sonar",
			Sources: []models.Source{{Title: "Doc", URL: "https://www.example.com/x?sig=abc", Snippet: "s", Favicon: "f"}},
		}},
	}
	want, err := export.ThreadPDFFingerprint(thread)
	if err != nil {
		t.Fatal(err)
	}
	wantPDF, err := export.GenerateThreadPDF(thread)
	if err != nil {
		t.Fatal(err)
	}

	request, err := json.Marshal(pdfRenderRequest{Thread: *thread})
	if err != nil {
		t.Fatal(err)
	}
	var decoded pdfRenderRequest
	if err := json.Unmarshal(request, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, err := export.ThreadPDFFingerprint(&decoded.Thread); err != nil || got != want {
		t.Fatalf("round-tripped fingerprint = %q, %v; want %q", got, err, want)
	}

	var out bytes.Buffer
	if err := runPDFRenderHelper(bytes.NewReader(request), &out); err != nil {
		t.Fatalf("runPDFRenderHelper: %v", err)
	}
	if !bytes.Equal(out.Bytes(), wantPDF) {
		t.Fatal("helper PDF differs from in-process PDF for the same fingerprint")
	}
}

func TestExportPDFFailedRenderDropsFingerprint(t *testing.T) {
	dir := t.TempDir()
	threads := pdfSkipTestThreads()[:1]
	exp := export.NewPDFExporter(dir)

	countPDFRenders(t, "write")
	cmd := &ExportCmd{Output: dir, PDFTimeout: time.Minute}
	if err := cmd.exportFormat(context.Background(), "pdf", threads, nil, nil, nil); err != nil {
		t.Fatalf("initial exportFormat: %v", err)
	}

	threads[0].Entries[0].Answer = "changed"
	countPDFRenders(t, "empty")
	if err := cmd.exportFormat(context.Background(), "pdf", threads, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "produced an empty file") {
		t.Fatalf("exportFormat error = %v, want empty output error", err)
	}
	if _, err := os.Stat(exp.ThreadPDFFingerprintPath(&threads[0])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fingerprint remains after failed render: %v", err)
	}
	if data, err := os.ReadFile(exp.ThreadPDFPath(&threads[0])); err != nil || string(data) != "rendered:thread-a" {
		t.Fatalf("previous PDF = %q, %v; want it left in place", data, err)
	}

	count := countPDFRenders(t, "write")
	if err := cmd.exportFormat(context.Background(), "pdf", threads, nil, nil, nil); err != nil {
		t.Fatalf("retry exportFormat: %v", err)
	}
	if count.Load() != 1 {
		t.Fatalf("retry PDF renders = %d, want 1", count.Load())
	}
	assertPDFsUpToDate(t, exp, threads)
}

func TestForcePDFFlag(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{args: []string{"export"}, want: false},
		{args: []string{"export", "--force-pdf"}, want: true},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		if cli.Export.ForcePDF != tt.want {
			t.Fatalf("%v: ForcePDF = %t, want %t", tt.args, cli.Export.ForcePDF, tt.want)
		}
	}
}

func TestExportHelpExplainsIncrementalPDF(t *testing.T) {
	var cli CLI
	var out bytes.Buffer
	parser, err := kong.New(&cli, kong.Name("deplexity"), kong.Writers(&out, &out), kong.Exit(func(int) {}))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = parser.Parse([]string{"export", "--help"})
	// Kong wraps help text, so compare with whitespace collapsed.
	help := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{"--force-pdf", "PDF export is incremental", ".thread.pdf.fingerprint", "--refresh re-fetches thread data"} {
		if !strings.Contains(help, want) {
			t.Errorf("export --help does not mention %q:\n%s", want, help)
		}
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
			{UUID: "old-1", Slug: "previous-old-slug", UpdatedAt: previousUpdatedAt, RetryRequired: true},
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
			{UUID: "promoted", Slug: "promoted-slug", Title: "Promoted"},
			{UUID: "old-1", Slug: "fresh-old-slug", Title: "Old"},
			{UUID: "tail", Slug: "tail-slug", Title: "Tail"},
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
	if !refs[1].RetryRequired || !refs[1].PreviousUpdatedAt.Equal(previousUpdatedAt) || refs[1].PreviousSlug != "previous-old-slug" {
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
	if refs[1].Slug != "fresh-old-slug" || reloaded.Threads[1].Slug != "fresh-old-slug" {
		t.Fatalf("fresh slug was not persisted: refs=%#v index=%#v", refs[1], reloaded.Threads[1])
	}
}

func TestFetchThreadIndexServesCompleteCacheWithoutListing(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	index := &models.ThreadIndex{
		Threads:   []models.ThreadRef{{UUID: "cached", Slug: "cached-slug"}},
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
	if refs[0].Slug != "cached-slug" {
		t.Errorf("cached slug = %q, want cached-slug", refs[0].Slug)
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
			{UUID: "deep-retry", Slug: "deep-slug", RetryRequired: true},
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
	if reloaded.Threads[2].Slug != "deep-slug" {
		t.Errorf("unseen prior slug = %q, want deep-slug", reloaded.Threads[2].Slug)
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

func TestFetchThreadDetailsPreservesSlugThroughCheckpointAndManifest(t *testing.T) {
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	ref := models.ThreadRef{UUID: "thread-1", Slug: "human-readable-slug", Title: "List title", SpaceUUID: "space-1"}
	fetch := func(_ context.Context, uuid string, _ *models.Thread, onPage func(*models.Thread) error) (*models.Thread, error) {
		checkpoint := &models.Thread{UUID: uuid, Slug: uuid, NextCursor: "next"}
		if err := onPage(checkpoint); err != nil {
			return nil, err
		}
		return &models.Thread{UUID: uuid, Slug: uuid, Complete: true}, nil
	}

	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, []models.ThreadRef{ref}, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(failures) != 0 || len(threads) != 1 {
		t.Fatalf("threads=%#v failures=%#v", threads, failures)
	}
	got := threads[0]
	if got.Slug != ref.Slug || got.SpaceUUID != ref.SpaceUUID || got.Title != ref.Title {
		t.Errorf("thread metadata = %#v, want list metadata", got)
	}
	loaded, err := jsonExp.LoadCompleteThread(ref)
	if err != nil {
		t.Fatalf("LoadCompleteThread: %v", err)
	}
	if loaded.Slug != ref.Slug {
		t.Errorf("cached slug = %q, want %q", loaded.Slug, ref.Slug)
	}
	manifest := buildManifest([]string{"json"}, threads, []models.ThreadRef{ref}, nil, nil, nil)
	if manifest.ThreadIndex[ref.UUID] != ref.Slug {
		t.Errorf("manifest index = %#v, want original list slug", manifest.ThreadIndex)
	}
}

func TestFetchThreadDetailsRefetchesLegacyCachesFromStart(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		cursor   string
	}{
		{name: "complete cache", complete: true},
		{name: "partial checkpoint", cursor: "legacy-cursor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			legacyDir := filepath.Join(dir, "threads", "thread-1")
			if err := os.MkdirAll(legacyDir, 0755); err != nil {
				t.Fatalf("create legacy directory: %v", err)
			}
			legacy := models.Thread{UUID: "thread-1", Slug: "thread-1", Complete: tt.complete, NextCursor: tt.cursor}
			data, err := json.Marshal(legacy)
			if err != nil {
				t.Fatalf("marshal legacy cache: %v", err)
			}
			if err := os.WriteFile(filepath.Join(legacyDir, "thread.json"), data, 0600); err != nil {
				t.Fatalf("write legacy cache: %v", err)
			}
			jsonExp := &export.JSONExporter{OutputDir: dir}
			ref := models.ThreadRef{UUID: "thread-1", Slug: "human-readable-slug"}
			var called bool
			fetch := func(_ context.Context, uuid string, resume *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
				called = true
				if resume != nil {
					t.Fatalf("resume = %#v, want legacy cache fetched from page one", resume)
				}
				return &models.Thread{UUID: uuid, Slug: uuid, Bookmarked: true, Complete: true}, nil
			}

			cmd := &ExportCmd{}
			threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, []models.ThreadRef{ref}, fetch)
			if err != nil {
				t.Fatalf("fetchThreadDetails: %v", err)
			}
			if !called {
				t.Fatal("fetch was not called for legacy cache")
			}
			if len(failures) != 0 || len(threads) != 1 || threads[0].Slug != ref.Slug || !threads[0].Bookmarked {
				t.Fatalf("threads=%#v failures=%#v", threads, failures)
			}
			if !jsonExp.HasCanonicalThread(ref) {
				t.Fatal("legacy cache was not written to the canonical path")
			}
			if _, err := os.Stat(filepath.Join(legacyDir, "thread.json")); err != nil {
				t.Errorf("legacy cache was removed: %v", err)
			}
		})
	}
}

func TestFetchThreadDetailsRehomesCacheAfterSlugChange(t *testing.T) {
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	old := &models.Thread{UUID: "thread-1", Slug: "old-slug", Complete: true}
	if err := jsonExp.ExportThread(old); err != nil {
		t.Fatalf("seed old canonical cache: %v", err)
	}
	ref := models.ThreadRef{UUID: "thread-1", Slug: "new-slug", PreviousSlug: "old-slug"}
	fetch := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		t.Fatal("fetch called for complete previous-slug cache")
		return nil, nil
	}

	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, []models.ThreadRef{ref}, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(failures) != 0 || len(threads) != 1 || threads[0].Slug != ref.Slug {
		t.Fatalf("threads=%#v failures=%#v", threads, failures)
	}
	if !jsonExp.HasCanonicalThread(ref) {
		t.Fatal("previous-slug cache was not written to the current canonical path")
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

func TestFetchThreadDetailsReportsStaleSourcesCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	oldUpdatedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	stale := &models.Thread{
		UUID:      "thread-1",
		Slug:      "thread-1",
		UpdatedAt: oldUpdatedAt,
		Complete:  true,
		Entries:   []models.Entry{{Sources: []models.Source{{URL: "https://example.com"}}}},
	}
	if err := jsonExp.ExportThread(stale); err != nil {
		t.Fatalf("seed stale thread: %v", err)
	}
	sourcePaths, err := filepath.Glob(filepath.Join(dir, "threads", "*", "sources.json"))
	if err != nil {
		t.Fatalf("find sources file: %v", err)
	}
	if len(sourcePaths) != 1 {
		t.Fatalf("source paths = %v, want one", sourcePaths)
	}
	sourcesPath := sourcePaths[0]
	if err := os.Remove(sourcesPath); err != nil {
		t.Fatalf("remove sources file: %v", err)
	}
	if err := os.Mkdir(sourcesPath, 0755); err != nil {
		t.Fatalf("create blocking sources directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourcesPath, "keep"), []byte("blocked"), 0600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	refs := []models.ThreadRef{{
		UUID:              stale.UUID,
		Slug:              stale.Slug,
		UpdatedAt:         oldUpdatedAt.Add(time.Hour),
		PreviousUpdatedAt: oldUpdatedAt,
	}}
	fetch := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		return &models.Thread{UUID: stale.UUID, Slug: stale.Slug, UpdatedAt: refs[0].UpdatedAt, Complete: true}, nil
	}

	cmd := &ExportCmd{Refresh: true}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil {
		t.Fatalf("fetchThreadDetails: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("thread counted as successful after cleanup failure: %#v", threads)
	}
	if len(failures) != 1 || failures[0].Stage != models.ThreadExportStageWrite || !strings.Contains(failures[0].Error, "remove stale sources") {
		t.Fatalf("failures = %#v, want stale sources write failure", failures)
	}
	ref := models.ThreadRef{UUID: stale.UUID, Slug: stale.Slug}
	if _, err := jsonExp.LoadCompleteThread(ref); err == nil {
		t.Fatal("LoadCompleteThread accepted cache after cleanup failure")
	}
	partial, err := jsonExp.LoadThread(ref)
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if partial.Complete {
		t.Fatal("thread cache remains complete after cleanup failure")
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
			UUID:       "thread-1",
			Slug:       "first",
			Bookmarked: true,
			Complete:   true,
			Entries:    []models.Entry{{Sources: []models.Source{{URL: "https://example.com"}}}},
		},
		{UUID: "thread-2", Slug: "second", Complete: true},
	}
	failures := []models.ThreadExportFailure{{UUID: "thread-3", Stage: models.ThreadExportStageFetch, Error: "failed"}}
	account := &models.Account{GlobalSkills: []models.Skill{{ID: "skill-1"}}}

	refs := []models.ThreadRef{{UUID: "thread-1", Slug: "first"}, {UUID: "thread-2", Slug: "second"}, {UUID: "thread-3", Slug: "failed-slug"}}
	manifest := buildManifest([]string{"json"}, threads, refs, []models.Space{{UUID: "space-1"}}, account, failures)
	if manifest.ThreadsComplete {
		t.Fatal("ThreadsComplete = true, want false")
	}
	if manifest.ExpectedThreads != 3 || manifest.Counts.Threads != 2 {
		t.Errorf("expected=%d exported=%d, want 3 and 2", manifest.ExpectedThreads, manifest.Counts.Threads)
	}
	if manifest.Counts.Spaces != 1 || manifest.Counts.Sources != 1 || manifest.Counts.Bookmarks != 1 || manifest.Counts.GlobalSkills != 1 {
		t.Errorf("counts = %#v, want one space, source, bookmark, and global skill", manifest.Counts)
	}
	if len(manifest.FailedThreads) != 1 || manifest.FailedThreads[0].UUID != "thread-3" {
		t.Errorf("FailedThreads = %#v, want thread-3", manifest.FailedThreads)
	}
	if manifest.ThreadIndex["thread-1"] != "first" {
		t.Errorf("ThreadIndex = %#v, want exported thread", manifest.ThreadIndex)
	}
	if manifest.ThreadIndex["thread-3"] != "failed-slug" {
		t.Errorf("ThreadIndex = %#v, want failed thread list slug preserved", manifest.ThreadIndex)
	}

	complete := buildManifest([]string{"json"}, nil, nil, nil, nil, nil)
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
