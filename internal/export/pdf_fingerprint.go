package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	gpdf "github.com/gpdf-dev/gpdf"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// pdfLayoutVersion salts every PDF fingerprint. Bump it whenever
// GenerateThreadPDF changes how a thread is laid out or styled so that
// existing PDFs are re-rendered on the next export.
const pdfLayoutVersion = 1

const (
	pdfFingerprintFile    = ".thread.pdf.fingerprint"
	pdfFingerprintPrefix  = "v1:sha256:"
	maxPDFFingerprintSize = 256
)

// pdfRenderInput holds exactly the values GenerateThreadPDF renders. The PDF
// fingerprint hashes this struct, so fields that do not appear in the PDF
// (snippets, favicons, full source URLs, update timestamps) never force a
// re-render.
type pdfRenderInput struct {
	MetadataTitle string           `json:"metadata_title"`
	Title         string           `json:"title"`
	Created       string           `json:"created"`
	Entries       []pdfRenderEntry `json:"entries"`
}

type pdfRenderEntry struct {
	Query   string   `json:"query"`
	Answer  string   `json:"answer"`
	Sources []string `json:"sources"`
	Model   string   `json:"model"`
}

func newPDFRenderInput(thread *models.Thread) pdfRenderInput {
	input := pdfRenderInput{
		MetadataTitle: thread.Title,
		Title:         thread.Title,
		Entries:       make([]pdfRenderEntry, len(thread.Entries)),
	}
	if input.Title == "" {
		input.Title = "Untitled Thread"
	}
	if !thread.CreatedAt.IsZero() {
		input.Created = thread.CreatedAt.Format("January 2, 2006")
	}
	for i, entry := range thread.Entries {
		sources := make([]string, len(entry.Sources))
		for j, src := range entry.Sources {
			sources[j] = formatSourceLine(j+1, src.Title, src.URL)
		}
		input.Entries[i] = pdfRenderEntry{
			Query:   entry.Query,
			Answer:  entry.Answer,
			Sources: sources,
			Model:   entry.Model,
		}
	}
	return input
}

// ThreadPDFFingerprint returns a versioned hash of everything that determines
// a thread's PDF: the rendered thread content, the PDF layout version, and the
// gpdf library version.
func ThreadPDFFingerprint(thread *models.Thread) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "deplexity-pdf\x00layout=%d\x00gpdf=%s\x00", pdfLayoutVersion, gpdf.Version)
	if err := json.NewEncoder(h).Encode(newPDFRenderInput(thread)); err != nil {
		return "", fmt.Errorf("could not fingerprint PDF input for thread %q (%s): %w", thread.Title, thread.UUID, err)
	}
	return pdfFingerprintPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// ThreadPDFFingerprintPath returns the path of the fingerprint recorded for a
// thread's canonical PDF.
func (e *PDFExporter) ThreadPDFFingerprintPath(thread *models.Thread) string {
	return filepath.Join(filepath.Dir(e.ThreadPDFPath(thread)), pdfFingerprintFile)
}

// PDFUpToDate reports whether the canonical PDF for thread is a non-empty
// regular file (not a symlink) rendered from inputs matching fingerprint.
func (e *PDFExporter) PDFUpToDate(thread *models.Thread, fingerprint string) bool {
	info, err := os.Lstat(e.ThreadPDFPath(thread))
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	stored, err := readPDFFingerprint(e.ThreadPDFFingerprintPath(thread))
	return err == nil && stored == fingerprint
}

// RemovePDFFingerprint deletes the recorded fingerprint so the thread's PDF is
// treated as stale until a new one is published. A missing file is not an
// error.
func (e *PDFExporter) RemovePDFFingerprint(thread *models.Thread) error {
	if err := os.Remove(e.ThreadPDFFingerprintPath(thread)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not remove PDF fingerprint for thread %q (%s): %w", thread.Title, thread.UUID, err)
	}
	return nil
}

// WritePDFFingerprint atomically records the fingerprint of the thread's
// published PDF. Call it only after the PDF itself has been published.
func (e *PDFExporter) WritePDFFingerprint(thread *models.Thread, fingerprint string) error {
	fail := func(action string, err error) error {
		return fmt.Errorf("could not %s PDF fingerprint for thread %q (%s): %w", action, thread.Title, thread.UUID, err)
	}
	path := e.ThreadPDFFingerprintPath(thread)
	tmp, err := os.CreateTemp(filepath.Dir(path), pdfFingerprintFile+".tmp-*")
	if err != nil {
		return fail("create temporary", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(fingerprint + "\n"); err != nil {
		tmp.Close()
		return fail("write", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail("flush", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fail("set permissions on", err)
	}
	if err := tmp.Close(); err != nil {
		return fail("close temporary", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fail("publish", err)
	}
	return nil
}

func readPDFFingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPDFFingerprintSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxPDFFingerprintSize {
		return "", fmt.Errorf("PDF fingerprint %s exceeds %d bytes", path, maxPDFFingerprintSize)
	}
	return string(bytes.TrimSpace(data)), nil
}
