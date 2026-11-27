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

	"github.com/clappingmonkey/deplexity/internal/auth"
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
		{name: "unchanged without refresh", ref: models.ThreadRef{UpdatedAt: current, FetchedUpdatedAt: previous}, cached: cached, want: false},
		{name: "unchanged with refresh", refresh: true, ref: models.ThreadRef{UpdatedAt: previous, FetchedUpdatedAt: previous}, cached: cached, want: false},
		{name: "newer with refresh", refresh: true, ref: models.ThreadRef{UpdatedAt: current, FetchedUpdatedAt: previous}, cached: cached, want: true},
		{name: "unknown current timestamp", refresh: true, ref: models.ThreadRef{FetchedUpdatedAt: previous}, cached: cached, want: true},
		{name: "no fetched watermark", refresh: true, ref: models.ThreadRef{UpdatedAt: current}, cached: cached, want: true},
		{name: "nil cached thread", refresh: true, ref: models.ThreadRef{UpdatedAt: current, FetchedUpdatedAt: previous}, want: true},
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout error %v does not wrap context.DeadlineExceeded", err)
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

func TestLoginCookieFromEnvironment(t *testing.T) {
	for _, tt := range []struct {
		env  string
		args []string
		want string
	}{
		{args: []string{"login"}, want: ""},
		{env: "env-token", args: []string{"login"}, want: "env-token"},
		{env: "env-token", args: []string{"login", "--cookie", "-"}, want: "-"},
	} {
		t.Setenv("DEPLEXITY_COOKIE", tt.env)
		var cli CLI
		parser, err := kong.New(&cli)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		if cli.Login.Cookie != tt.want {
			t.Fatalf("env %q %v: Cookie = %q, want %q", tt.env, tt.args, cli.Login.Cookie, tt.want)
		}
	}
}

func TestBrowserLoginConfirmsSession(t *testing.T) {
	captured := &models.SavedSession{SessionToken: "tok"}
	stub := func(t *testing.T, confirm func(context.Context, *models.SavedSession) (*auth.SessionInfo, error)) {
		oldLogin, oldConfirm := browserLoginFunc, confirmSessionFunc
		t.Cleanup(func() { browserLoginFunc, confirmSessionFunc = oldLogin, oldConfirm })
		browserLoginFunc = func(context.Context, string) (*models.SavedSession, error) { return captured, nil }
		confirmSessionFunc = confirm
	}

	t.Run("confirmed", func(t *testing.T) {
		stub(t, func(context.Context, *models.SavedSession) (*auth.SessionInfo, error) {
			return &auth.SessionInfo{Email: "a@b"}, nil
		})
		if got, err := (&LoginCmd{}).browserLogin(context.Background()); err != nil || got != captured {
			t.Fatalf("browserLogin = %v, %v", got, err)
		}
	})
	t.Run("rejected is not saved", func(t *testing.T) {
		stub(t, func(context.Context, *models.SavedSession) (*auth.SessionInfo, error) {
			return nil, auth.ErrSessionRejected
		})
		if got, err := (&LoginCmd{}).browserLogin(context.Background()); !errors.Is(err, auth.ErrSessionRejected) || got != nil {
			t.Fatalf("browserLogin = %v, %v", got, err)
		}
	})
	t.Run("check failure keeps session", func(t *testing.T) {
		stub(t, func(context.Context, *models.SavedSession) (*auth.SessionInfo, error) {
			return nil, errors.New("dial failed")
		})
		if got, err := (&LoginCmd{}).browserLogin(context.Background()); err != nil || got != captured {
			t.Fatalf("browserLogin = %v, %v", got, err)
		}
	})
	t.Run("check failure after cancel is not saved", func(t *testing.T) {
		stub(t, func(context.Context, *models.SavedSession) (*auth.SessionInfo, error) {
			return nil, errors.New("dial failed")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := (&LoginCmd{}).browserLogin(ctx); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("browserLogin = %v, %v", got, err)
		}
	})
}

func TestLoginReadsTokenFromStdin(t *testing.T) {
	cmd := &LoginCmd{Cookie: "-", stdin: strings.NewReader("  tok-1  \nignored\n")}
	got, err := cmd.readStdinToken(context.Background())
	if err != nil || got != "tok-1" {
		t.Fatalf("readStdinToken = %q, %v", got, err)
	}
	cmd.stdin = strings.NewReader("")
	if _, err := cmd.readStdinToken(context.Background()); err == nil {
		t.Fatal("empty stdin accepted")
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
	listed := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	newer := listed.Add(time.Hour)
	partial := &models.Thread{NextCursor: "c1", ListedUpdatedAt: listed}
	ref := models.ThreadRef{UpdatedAt: listed}

	tests := []struct {
		name    string
		refresh bool
		partial *models.Thread
		ref     models.ThreadRef
		want    bool
	}{
		{name: "interrupted fetch", partial: partial, ref: ref, want: true},
		{name: "same instant in another zone", partial: partial, ref: models.ThreadRef{UpdatedAt: listed.In(time.FixedZone("x", 3600))}, want: true},
		{name: "refresh discards checkpoint", refresh: true, partial: partial, ref: ref},
		{name: "no partial on disk", ref: ref},
		{name: "already complete", partial: &models.Thread{Complete: true, NextCursor: "c1", ListedUpdatedAt: listed}, ref: ref},
		{name: "no progress recorded", partial: &models.Thread{ListedUpdatedAt: listed}, ref: ref},
		{name: "thread changed since checkpoint", partial: partial, ref: models.ThreadRef{UpdatedAt: newer}},
		{name: "checkpoint without listing time", partial: &models.Thread{NextCursor: "c1"}, ref: ref},
		{name: "listing without timestamp", partial: partial},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resumePoint(tt.refresh, tt.partial, tt.ref)
			if (got != nil) != tt.want {
				t.Errorf("resumePoint() = %v, want non-nil %t", got, tt.want)
			}
		})
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
	manifest := buildManifest([]string{"json"}, threads, []models.ThreadRef{ref}, nil, nil, nil, nil)
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
	if len(failures) != 1 || failures[0].UUID != "thread-1" || failures[0].Stage != models.ThreadExportStageWrite {
		t.Fatalf("failures = %#v, want a write failure recorded for thread-1", failures)
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
		UUID:             "thread-1",
		Title:            "First",
		UpdatedAt:        oldUpdatedAt.Add(time.Hour),
		FetchedUpdatedAt: oldUpdatedAt,
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
		UUID:             stale.UUID,
		Slug:             stale.Slug,
		UpdatedAt:        oldUpdatedAt.Add(time.Hour),
		FetchedUpdatedAt: oldUpdatedAt,
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
	manifest := buildManifest([]string{"json"}, threads, refs, []models.Space{{UUID: "space-1"}}, account, failures, nil)
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

	complete := buildManifest([]string{"json"}, nil, nil, nil, nil, nil, nil)
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

	err := finalizeExport(context.Background(), jsonExp, manifest)
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

func TestAttachPreviousUpdatedAt(t *testing.T) {
	listed := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fetched := listed.Add(-time.Hour)
	refs := []models.ThreadRef{{UUID: "watermarked", Slug: "new-slug"}, {UUID: "legacy"}, {UUID: "new"}}
	attachPreviousUpdatedAt(refs, []models.ThreadRef{
		{UUID: "watermarked", Slug: "old-slug", UpdatedAt: listed, FetchedUpdatedAt: fetched, CacheSlug: "cached-slug", RetryRequired: true},
		{UUID: "legacy", Slug: "legacy-slug", UpdatedAt: listed},
	})

	if got := refs[0]; !got.FetchedUpdatedAt.Equal(fetched) || !got.RetryRequired || got.CacheSlug != "cached-slug" || got.PreviousSlug != "old-slug" {
		t.Errorf("watermarked ref = %+v, want the saved watermark, retry flag and slugs", got)
	}
	// LoadThreadIndex migrates old indexes; here a missing watermark must stay
	// missing instead of taking the listing time.
	if got := refs[1]; !got.FetchedUpdatedAt.IsZero() || got.CacheSlug != "" || got.PreviousSlug != "legacy-slug" {
		t.Errorf("unknown-watermark ref = %+v, want no watermark", got)
	}
	if got := refs[2]; !got.FetchedUpdatedAt.IsZero() || got.CacheSlug != "" || got.RetryRequired {
		t.Errorf("new ref = %+v, want no carried state", got)
	}
}

// An interrupted --refresh used to save the new listing time as if the detail
// had been fetched, so the next --refresh skipped the stale cached thread.
func TestInterruptedRefreshStillRefetchesChangedThread(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	if err := jsonExp.ExportThread(&models.Thread{UUID: "a", Slug: "a", UpdatedAt: t0, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := jsonExp.SaveThreadIndex(&models.ThreadIndex{
		Threads:  []models.ThreadRef{{UUID: "a", Slug: "a", UpdatedAt: t0, FetchedUpdatedAt: t0}},
		Total:    1,
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	listed := []models.Thread{{UUID: "a", Slug: "a", UpdatedAt: t1}}
	cmd := &ExportCmd{Refresh: true}

	// Run 1: the listing sees the change, then fails on a later page.
	_, err := cmd.freshListThreads(context.Background(), jsonExp, func(context.Context, func(int)) ([]models.Thread, error) {
		return listed, errors.New("page 2 failed")
	})
	if err == nil {
		t.Fatal("freshListThreads: want the listing error")
	}

	// Run 2: the listing succeeds with the same timestamp.
	refs, err := cmd.freshListThreads(context.Background(), jsonExp, func(context.Context, func(int)) ([]models.Thread, error) {
		return listed, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var fetched []string
	getThread := func(_ context.Context, uuid string, _ *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
		fetched = append(fetched, uuid)
		return &models.Thread{UUID: uuid, UpdatedAt: t1, Complete: true}, nil
	}
	if _, _, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, getThread); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fetched, []string{"a"}) {
		t.Fatalf("fetched = %v, want the changed thread fetched again", fetched)
	}
	if refs[0].RetryRequired || !refs[0].FetchedUpdatedAt.Equal(t1) {
		t.Errorf("ref after fetch = %+v, want retry cleared and watermark t1", refs[0])
	}
}

func TestRefreshWithoutChangesFetchesNothing(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := jsonExp.ExportThread(&models.Thread{UUID: "a", Slug: "a", UpdatedAt: t0, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := jsonExp.SaveThreadIndex(&models.ThreadIndex{
		Threads:  []models.ThreadRef{{UUID: "a", Slug: "a", UpdatedAt: t0, FetchedUpdatedAt: t0}},
		Total:    1,
		Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	cmd := &ExportCmd{Refresh: true}
	refs, err := cmd.freshListThreads(context.Background(), jsonExp, func(context.Context, func(int)) ([]models.Thread, error) {
		return []models.Thread{{UUID: "a", Slug: "a", UpdatedAt: t0}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	getThread := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		t.Fatal("unchanged thread was fetched again")
		return nil, nil
	}
	threads, _, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, getThread)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %d, err = %v; want the cached thread", len(threads), err)
	}
}

func TestFetchThreadDetailsCanceledWithEverythingCached(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	if err := jsonExp.ExportThread(&models.Thread{UUID: "a", Slug: "a", Complete: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	getThread := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		t.Fatal("canceled run fetched a thread")
		return nil, nil
	}
	cmd := &ExportCmd{}
	_, _, err := cmd.fetchThreadDetails(ctx, jsonExp, []models.ThreadRef{{UUID: "a", Slug: "a"}}, getThread)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestApplyThreadRefMetadataSpaceMembership(t *testing.T) {
	thread := &models.Thread{SpaceUUID: "space-a"}
	if applyThreadRefMetadata(thread, models.ThreadRef{}) || thread.SpaceUUID != "space-a" {
		t.Fatalf("legacy ref changed the space to %q", thread.SpaceUUID)
	}
	if !applyThreadRefMetadata(thread, models.ThreadRef{SpaceKnown: true}) || thread.SpaceUUID != "" {
		t.Fatalf("current listing without a space left %q", thread.SpaceUUID)
	}
}

func TestFinalizeExport(t *testing.T) {
	t.Run("canceled writes no manifest", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "out")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := finalizeExport(ctx, &export.JSONExporter{OutputDir: dir}, buildManifest(nil, nil, nil, nil, nil, nil, nil))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
			t.Fatalf("manifest written for a canceled run: %v", err)
		}
	})
	t.Run("creates the output dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "fresh", "out")
		if err := finalizeExport(context.Background(), &export.JSONExporter{OutputDir: dir}, buildManifest(nil, nil, nil, nil, nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("section failures exit non-zero", func(t *testing.T) {
		dir := t.TempDir()
		sections := []models.SectionFailure{{Section: models.SectionSpaceDetail, Item: "space", Error: "boom"}}
		err := finalizeExport(context.Background(), &export.JSONExporter{OutputDir: dir}, buildManifest(nil, nil, nil, nil, nil, nil, sections))
		if !errors.Is(err, errIncompleteSections) || errors.Is(err, errIncompleteThreadExport) {
			t.Fatalf("err = %v, want only errIncompleteSections", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m models.ExportManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		if m.SectionsComplete || len(m.FailedSections) != 1 {
			t.Fatalf("manifest sections = %v %+v", m.SectionsComplete, m.FailedSections)
		}
	})
}

// A thread whose fetch watermark is unknown must be fetched on the next
// --refresh, even after a plain run re-listed it without fetching.
func TestUnknownWatermarkSurvivesPlainRelist(t *testing.T) {
	jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
	t1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := jsonExp.ExportThread(&models.Thread{UUID: "a", Slug: "a", Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := jsonExp.SaveThreadIndex(&models.ThreadIndex{Threads: []models.ThreadRef{{UUID: "a", Slug: "a"}}, Total: 1}); err != nil {
		t.Fatal(err)
	}
	list := func(context.Context, func(int)) ([]models.Thread, error) {
		return []models.Thread{{UUID: "a", Slug: "a", UpdatedAt: t1}}, nil
	}
	if _, err := (&ExportCmd{}).freshListThreads(context.Background(), jsonExp, list); err != nil {
		t.Fatal(err)
	}
	refs, err := (&ExportCmd{Refresh: true}).freshListThreads(context.Background(), jsonExp, list)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := jsonExp.LoadCompleteThread(refs[0])
	if !needsThreadFetch(true, refs[0], cached, err) {
		t.Fatalf("ref = %+v, want a fetch because the cached detail's version is unknown", refs[0])
	}
}

// A listing without a slug must not make the run forget where the detail is.
func TestFetchThreadDetailsKeepsCacheSlugWhenListingHasNone(t *testing.T) {
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	if err := jsonExp.ExportThread(&models.Thread{UUID: "thread-1", Slug: "old-slug", Complete: true}); err != nil {
		t.Fatal(err)
	}
	refs := []models.ThreadRef{{UUID: "thread-1", CacheSlug: "old-slug"}}
	fetch := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
		t.Fatal("fetch called for a cached thread")
		return nil, nil
	}
	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil || len(failures) != 0 || len(threads) != 1 {
		t.Fatalf("threads=%d failures=%v err=%v", len(threads), failures, err)
	}
	if refs[0].CacheSlug != "old-slug" {
		t.Fatalf("cache slug = %q, want old-slug kept", refs[0].CacheSlug)
	}
	if _, err := jsonExp.LoadThread(refs[0]); err != nil {
		t.Fatalf("cached thread no longer found: %v", err)
	}
}

// A checkpoint written under a new slug must win over the older copy under the
// previous cache slug, even when the fetch then fails.
func TestFetchThreadDetailsCheckpointMovesCacheSlug(t *testing.T) {
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	if err := jsonExp.ExportThread(&models.Thread{UUID: "thread-1", Slug: "a", Title: "STALE"}); err != nil {
		t.Fatal(err)
	}
	refs := []models.ThreadRef{{UUID: "thread-1", Slug: "b", CacheSlug: "a"}}
	fetch := func(_ context.Context, _ string, _ *models.Thread, onPage func(*models.Thread) error) (*models.Thread, error) {
		if err := onPage(&models.Thread{UUID: "thread-1", Slug: "b", Title: "NEW"}); err != nil {
			return nil, err
		}
		return nil, errors.New("page 2 failed")
	}
	cmd := &ExportCmd{}
	if _, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch); err != nil || len(failures) != 1 {
		t.Fatalf("failures=%v err=%v, want one fetch failure", failures, err)
	}
	if refs[0].CacheSlug != "b" {
		t.Fatalf("cache slug = %q, want b", refs[0].CacheSlug)
	}
	// Even an index that still says "a" (the run stopped before saving it)
	// must not find the older copy.
	got, err := jsonExp.LoadThread(models.ThreadRef{UUID: "thread-1", Slug: "b", CacheSlug: "a"})
	if err != nil || got.Title != "NEW" {
		t.Fatalf("LoadThread = %+v, %v; want the NEW checkpoint", got, err)
	}
}

// Slugs that name the same directory must not delete the cache just written.
func TestFetchThreadDetailsKeepsCacheWhenSlugsShareDirectory(t *testing.T) {
	for _, tc := range []struct{ old, current string }{
		{"My-Slug", "my-slug"},
		{"a--b", "a-b"},
	} {
		t.Run(tc.current, func(t *testing.T) {
			jsonExp := &export.JSONExporter{OutputDir: t.TempDir()}
			refs := []models.ThreadRef{{UUID: "thread-1", Slug: tc.current, CacheSlug: tc.old}}
			fetch := func(context.Context, string, *models.Thread, func(*models.Thread) error) (*models.Thread, error) {
				return &models.Thread{UUID: "thread-1", Slug: tc.current, Complete: true}, nil
			}
			cmd := &ExportCmd{}
			threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
			if err != nil || len(failures) != 0 || len(threads) != 1 {
				t.Fatalf("threads=%d failures=%v err=%v, want the fresh thread", len(threads), failures, err)
			}
		})
	}
}

// When the old copy cannot be removed, the index must still point at the new
// checkpoint, so a retry does not resume the older one.
func TestFetchThreadDetailsDropFailureKeepsNewCacheSlug(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	stale := &models.Thread{UUID: "thread-1", Slug: "a", Title: "STALE", NextCursor: "c1"}
	if err := jsonExp.ExportThread(stale); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "threads", "a-*"))
	if len(matches) != 1 {
		t.Fatalf("stale dirs = %v", matches)
	}
	staleDir := matches[0]
	refs := []models.ThreadRef{{UUID: "thread-1", Slug: "b", CacheSlug: "a"}}
	fetch := func(_ context.Context, _ string, _ *models.Thread, onPage func(*models.Thread) error) (*models.Thread, error) {
		if err := os.Chmod(staleDir, 0o500); err != nil {
			return nil, err
		}
		defer os.Chmod(staleDir, 0o700)
		return nil, onPage(&models.Thread{UUID: "thread-1", Slug: "b", Title: "CURRENT", NextCursor: "c2"})
	}
	cmd := &ExportCmd{}
	_, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil || len(failures) != 1 {
		t.Fatalf("failures=%v err=%v, want one failure", failures, err)
	}
	if refs[0].CacheSlug != "b" {
		t.Fatalf("cache slug = %q, want b", refs[0].CacheSlug)
	}
	got, err := jsonExp.LoadThread(refs[0])
	if err != nil || got.Title != "CURRENT" {
		t.Fatalf("LoadThread = %+v, %v; want the CURRENT checkpoint", got, err)
	}
}

// A checkpoint left by a fetch of an older version of the thread must not be
// resumed, or the export would mix old entries with a current watermark.
func TestFetchThreadDetailsDoesNotResumeCheckpointFromOlderListing(t *testing.T) {
	dir := t.TempDir()
	jsonExp := &export.JSONExporter{OutputDir: dir}
	noon := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	one := noon.Add(time.Hour)
	old := &models.Thread{
		UUID: "thread-1", Slug: "a", UpdatedAt: noon, NextCursor: "old-cursor", ListedUpdatedAt: noon,
		Entries: []models.Entry{{UUID: "e1", Query: "STALE"}},
	}
	if err := jsonExp.ExportThread(old); err != nil {
		t.Fatal(err)
	}
	refs := []models.ThreadRef{{UUID: "thread-1", Slug: "a", UpdatedAt: one, CacheSlug: "a", FetchedUpdatedAt: noon.Add(-time.Hour), RetryRequired: true}}

	var gotResume *models.Thread
	fetch := func(_ context.Context, _ string, resume *models.Thread, _ func(*models.Thread) error) (*models.Thread, error) {
		gotResume = resume
		return &models.Thread{UUID: "thread-1", Slug: "a", UpdatedAt: one, Complete: true,
			Entries: []models.Entry{{UUID: "e1", Query: "CURRENT"}}}, nil
	}
	cmd := &ExportCmd{}
	threads, failures, err := cmd.fetchThreadDetails(context.Background(), jsonExp, refs, fetch)
	if err != nil || len(failures) != 0 {
		t.Fatalf("failures=%v err=%v", failures, err)
	}
	if gotResume != nil {
		t.Fatalf("resumed from %q, want a fresh fetch", gotResume.NextCursor)
	}
	if len(threads) != 1 || threads[0].Entries[0].Query != "CURRENT" {
		t.Fatalf("threads = %+v, want CURRENT", threads)
	}
	if !refs[0].FetchedUpdatedAt.Equal(one) {
		t.Fatalf("watermark = %v, want %v", refs[0].FetchedUpdatedAt, one)
	}
	saved, err := jsonExp.LoadThread(refs[0])
	if err != nil || !saved.ListedUpdatedAt.Equal(one) {
		t.Fatalf("saved = %+v, %v; want listed_updated_at %v", saved, err, one)
	}
}
