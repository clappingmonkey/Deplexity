package export

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"github.com/clappingmonkey/deplexity/internal/models"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

const pdfHTMLTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
	body {
		font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
		font-size: 14px;
		line-height: 1.6;
		color: #1a1a1a;
		max-width: 800px;
		margin: 0 auto;
		padding: 40px;
	}
	h1 { font-size: 24px; margin-bottom: 8px; }
	h2 { font-size: 18px; color: #333; margin-top: 24px; }
	h3 { font-size: 15px; color: #555; }
	hr { border: none; border-top: 1px solid #ddd; margin: 24px 0; }
	a { color: #1a73e8; text-decoration: none; }
	blockquote {
		border-left: 3px solid #ddd;
		margin: 0;
		padding: 4px 16px;
		color: #555;
	}
	pre {
		background: #f5f5f5;
		border-radius: 4px;
		padding: 12px;
		overflow-x: auto;
		font-size: 13px;
	}
	code {
		background: #f0f0f0;
		border-radius: 3px;
		padding: 2px 4px;
		font-size: 13px;
	}
	pre code { background: none; padding: 0; }
	.metadata { color: #666; font-size: 12px; margin-bottom: 24px; }
	.sources { font-size: 13px; }
	.sources li { margin-bottom: 4px; }
	.model-info { font-size: 12px; color: #888; font-style: italic; }
</style>
</head>
<body>
{{.Content}}
</body>
</html>`

// PDFExporter generates PDF files from thread data.
type PDFExporter struct {
	OutputDir string
	browser   *rod.Browser
}

// NewPDFExporter creates a PDF exporter, launching a headless browser.
func NewPDFExporter(outputDir string) (*PDFExporter, error) {
	path, hasPath := launcher.LookPath()
	if !hasPath {
		return nil, fmt.Errorf("could not find Chrome/Chromium — PDF export requires Chrome")
	}

	u := launcher.New().
		Bin(path).
		Headless(true).
		MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()

	return &PDFExporter{
		OutputDir: outputDir,
		browser:   browser,
	}, nil
}

// Close shuts down the headless browser.
func (e *PDFExporter) Close() {
	if e.browser != nil {
		e.browser.MustClose()
	}
}

// ExportThread generates a PDF for a single thread.
func (e *PDFExporter) ExportThread(thread *models.Thread) error {
	dir := e.threadDir(thread)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}

	// Read the markdown file if it exists, otherwise generate markdown content
	mdPath := filepath.Join(dir, "thread.md")
	mdBytes, err := os.ReadFile(mdPath)
	if err != nil {
		// Generate markdown inline if the .md file doesn't exist yet
		mdBytes = []byte(threadToMarkdown(thread))
	}

	// Convert Markdown to HTML
	htmlContent, err := markdownToHTML(mdBytes)
	if err != nil {
		return fmt.Errorf("could not convert markdown to HTML: %w", err)
	}

	// Wrap in styled HTML template
	fullHTML, err := wrapHTML(htmlContent)
	if err != nil {
		return fmt.Errorf("could not generate HTML: %w", err)
	}

	// Render PDF via headless Chrome
	pdfBytes, err := e.renderPDF(fullHTML)
	if err != nil {
		return fmt.Errorf("could not render PDF: %w", err)
	}

	pdfPath := filepath.Join(dir, "thread.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0644); err != nil {
		return fmt.Errorf("could not write PDF file: %w", err)
	}

	return nil
}

// renderPDF uses headless Chrome to convert HTML to PDF.
func (e *PDFExporter) renderPDF(htmlContent string) ([]byte, error) {
	page := e.browser.MustPage("")
	defer page.MustClose()

	if err := page.SetDocumentContent(htmlContent); err != nil {
		return nil, fmt.Errorf("could not set page content: %w", err)
	}

	page.MustWaitStable()

	pdf, err := page.PDF(&proto.PagePrintToPDF{
		PrintBackground: true,
		PaperWidth:      floatPtr(8.5),
		PaperHeight:     floatPtr(11.0),
		MarginTop:       floatPtr(0.5),
		MarginBottom:    floatPtr(0.5),
		MarginLeft:      floatPtr(0.5),
		MarginRight:     floatPtr(0.5),
	})
	if err != nil {
		return nil, fmt.Errorf("PDF generation failed: %w", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(pdf); err != nil {
		return nil, fmt.Errorf("could not read PDF data: %w", err)
	}

	return buf.Bytes(), nil
}

// markdownToHTML converts markdown bytes to an HTML fragment.
func markdownToHTML(md []byte) (string, error) {
	gm := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
		),
	)

	var buf bytes.Buffer
	if err := gm.Convert(md, &buf); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// wrapHTML wraps an HTML fragment in the full PDF template.
func wrapHTML(content string) (string, error) {
	tmpl, err := template.New("pdf").Parse(pdfHTMLTemplate)
	if err != nil {
		return "", err
	}

	data := struct{ Content template.HTML }{
		Content: template.HTML(content),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// threadToMarkdown generates a markdown representation of a thread.
func threadToMarkdown(thread *models.Thread) string {
	var sb bytes.Buffer
	title := thread.Title
	if title == "" {
		title = "Untitled Thread"
	}
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))

	for i, entry := range thread.Entries {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		sb.WriteString(fmt.Sprintf("## Q: %s\n\n", entry.Query))
		sb.WriteString(fmt.Sprintf("%s\n\n", entry.Answer))

		if len(entry.Sources) > 0 {
			sb.WriteString("### Sources\n\n")
			for j, source := range entry.Sources {
				t := source.Title
				if t == "" {
					t = source.URL
				}
				sb.WriteString(fmt.Sprintf("%d. [%s](%s)\n", j+1, t, source.URL))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// threadDir returns the output directory for a thread.
func (e *PDFExporter) threadDir(thread *models.Thread) string {
	slug := thread.Slug
	if slug == "" {
		slug = thread.UUID
	}
	return filepath.Join(e.OutputDir, "threads", sanitizeFilename(slug))
}

// floatPtr returns a pointer to a float64 value.
func floatPtr(f float64) *float64 {
	return &f
}
