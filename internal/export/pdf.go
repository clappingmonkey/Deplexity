package export

import (
	"context"
	"fmt"
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
	dir := e.threadDir(thread)
	return writeThreadPDF(dir, thread)
}

// ExportSpaces generates PDFs for threads within each space folder.
func (e *PDFExporter) ExportSpaces(ctx context.Context, spaces []models.Space, threads []models.Thread) error {
	threadByUUID := make(map[string]*models.Thread, len(threads))
	for i := range threads {
		threadByUUID[threads[i].UUID] = &threads[i]
	}
	for _, space := range spaces {
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
			dir := filepath.Join(e.OutputDir, "spaces", sanitizeFilename(space.Name), "threads", sanitizeFilename(threadSlug(thread)))
			if err := writeThreadPDF(dir, thread); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeThreadPDF generates a PDF for a thread into the given directory.
func writeThreadPDF(dir string, thread *models.Thread) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}

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
		return fmt.Errorf("could not generate PDF: %w", err)
	}

	pdfPath := filepath.Join(dir, "thread.pdf")
	if err := os.WriteFile(pdfPath, data, 0644); err != nil {
		return fmt.Errorf("could not write PDF file: %w", err)
	}

	return nil
}

// threadDir returns the output directory for a thread.
func (e *PDFExporter) threadDir(thread *models.Thread) string {
	return filepath.Join(e.OutputDir, "threads", sanitizeFilename(threadSlug(thread)))
}