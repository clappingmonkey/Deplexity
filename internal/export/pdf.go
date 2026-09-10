package export

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	gpdf "github.com/gpdf-dev/gpdf"
	"github.com/gpdf-dev/gpdf/document"
	"github.com/gpdf-dev/gpdf/pdf"
	"github.com/gpdf-dev/gpdf/template"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// PDFExporter generates PDF files from thread data using pure Go (no browser).
type PDFExporter struct {
	OutputDir string
}

// domainFromURL extracts the hostname from a URL, stripping "www." prefix.
func domainFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := u.Host
	host = strings.TrimPrefix(host, "www.")
	return host
}

// formatSourceLine formats a source as "N. Title (domain)" or "N. domain" if no title.
func formatSourceLine(n int, title, rawURL string) string {
	domain := domainFromURL(rawURL)
	if title == "" {
		return fmt.Sprintf("%d. %s", n, domain)
	}
	return fmt.Sprintf("%d. %s (%s)", n, title, domain)
}

// NewPDFExporter creates a PDFExporter writing to the given output directory.
func NewPDFExporter(outputDir string) *PDFExporter {
	return &PDFExporter{OutputDir: outputDir}
}

// Close is a no-op kept for interface compatibility.
func (e *PDFExporter) Close() {}

// ExportThread generates a PDF for a single thread.
func (e *PDFExporter) ExportThread(thread *models.Thread) error {
	data, err := GenerateThreadPDF(thread)
	if err != nil {
		return err
	}
	path := e.ThreadPDFPath(thread)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".thread.pdf.tmp-*")
	if err != nil {
		return fmt.Errorf("could not create temporary PDF: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("could not write PDF file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("could not flush PDF file: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("could not set PDF permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close temporary PDF: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("could not publish PDF file: %w", err)
	}
	return nil
}

// ExportSpaces generates PDFs for threads within each space folder.
func (e *PDFExporter) ExportSpaces(ctx context.Context, spaces []models.Space, threads []models.Thread) error {
	threadByUUID := make(map[string]*models.Thread, len(threads))
	for i := range threads {
		threadByUUID[threads[i].UUID] = &threads[i]
	}
	spaceDirs := spaceDirNames(spaces)
	for i, space := range spaces {
		for _, uuid := range space.ThreadUUIDs {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			thread := threadByUUID[uuid]
			if thread == nil {
				continue
			}
			dst := filepath.Join(e.OutputDir, "spaces", spaceDirs[i], "threads", threadDirName(thread.Slug, thread.UUID), "thread.pdf")
			if err := copyFileContext(ctx, e.ThreadPDFPath(thread), dst); err != nil {
				return err
			}
		}
	}
	return nil
}

// GenerateThreadPDF generates PDF bytes for a thread.
func GenerateThreadPDF(thread *models.Thread) ([]byte, error) {
	doc := gpdf.NewDocument(
		gpdf.WithPageSize(gpdf.Letter),
		gpdf.WithMargins(document.Edges{
			Top:    document.In(0.75),
			Bottom: document.In(0.75),
			Left:   document.In(0.75),
			Right:  document.In(0.75),
		}),
		gpdf.WithMetadata(document.DocumentMetadata{
			Title:   thread.Title,
			Author:  "Deplexity",
			Subject: "Perplexity AI Thread Export",
		}),
	)

	doc.Header(func(p *template.PageBuilder) {
		p.AutoRow(func(r *template.RowBuilder) {
			r.Col(8, func(c *template.ColBuilder) {
				c.Text("Deplexity", template.FontSize(8), template.TextColor(pdf.Gray(0.5)))
			})
			r.Col(4, func(c *template.ColBuilder) {
				c.PageNumber(template.FontSize(8), template.AlignRight())
			})
		})
	})

	page := doc.AddPage()

	title := thread.Title
	if title == "" {
		title = "Untitled Thread"
	}

	page.AutoRow(func(r *template.RowBuilder) {
		r.Col(12, func(c *template.ColBuilder) {
			c.Text(title, template.FontSize(20), template.Bold())
		})
	})

	if !thread.CreatedAt.IsZero() {
		page.AutoRow(func(r *template.RowBuilder) {
			r.Col(12, func(c *template.ColBuilder) {
				c.Text(fmt.Sprintf("Created: %s", thread.CreatedAt.Format("January 2, 2006")), template.FontSize(9), template.TextColor(pdf.Gray(0.5)))
			})
		})
	}

	page.AutoRow(func(r *template.RowBuilder) {
		r.Col(12, func(c *template.ColBuilder) {
			c.Spacer(document.Mm(3))
		})
	})

	for i, entry := range thread.Entries {
		if i > 0 {
			page.AutoRow(func(r *template.RowBuilder) {
				r.Col(12, func(c *template.ColBuilder) {
					c.Spacer(document.Mm(5))
					c.Line()
					c.Spacer(document.Mm(5))
				})
			})
		}

		page.AutoRow(func(r *template.RowBuilder) {
			r.Col(12, func(c *template.ColBuilder) {
				c.RichText(func(rt *template.RichTextBuilder) {
					rt.Span("Q: ", template.Bold(), template.FontSize(13))
					rt.Span(entry.Query, template.FontSize(13))
				})
			})
		})

		page.AutoRow(func(r *template.RowBuilder) {
			r.Col(12, func(c *template.ColBuilder) {
				c.Spacer(document.Mm(2))
			})
		})

		page.AutoRow(func(r *template.RowBuilder) {
			r.Col(12, func(c *template.ColBuilder) {
				c.Text(entry.Answer, template.FontSize(10))
			})
		})

		if len(entry.Sources) > 0 {
			page.AutoRow(func(r *template.RowBuilder) {
				r.Col(12, func(c *template.ColBuilder) {
					c.Spacer(document.Mm(3))
					c.Text("Sources", template.Bold(), template.FontSize(11))
					c.Spacer(document.Mm(1))
					for j, src := range entry.Sources {
						label := formatSourceLine(j+1, src.Title, src.URL)
						c.Text(label, template.FontSize(9), template.TextColor(pdf.Gray(0.3)))
					}
				})
			})
		}

		if entry.Model != "" {
			page.AutoRow(func(r *template.RowBuilder) {
				r.Col(12, func(c *template.ColBuilder) {
					c.Spacer(document.Mm(2))
					c.Text(fmt.Sprintf("Model: %s", entry.Model), template.FontSize(8), template.TextColor(pdf.Gray(0.5)), template.Italic())
				})
			})
		}
	}

	data, err := doc.Generate()
	if err != nil {
		return nil, fmt.Errorf("could not generate PDF: %w", err)
	}
	return data, nil
}

// ThreadPDFPath returns the canonical PDF path for a thread.
func (e *PDFExporter) ThreadPDFPath(thread *models.Thread) string {
	return filepath.Join(e.OutputDir, "threads", threadDirName(thread.Slug, thread.UUID), "thread.pdf")
}

func copyFileContext(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("could not open source PDF: %w", err)
	}
	defer in.Close()
	srcInfo, err := in.Stat()
	if err != nil {
		return fmt.Errorf("could not inspect source PDF: %w", err)
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("source PDF is not a regular file: %s", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("could not create space thread directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".thread.pdf.tmp-*")
	if err != nil {
		return fmt.Errorf("could not create temporary PDF: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			tmp.Close()
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				tmp.Close()
				return fmt.Errorf("could not copy PDF: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmp.Close()
			return fmt.Errorf("could not read source PDF: %w", readErr)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("could not flush copied PDF: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("could not set PDF permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close temporary PDF: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("could not publish space PDF: %w", err)
	}
	return nil
}
