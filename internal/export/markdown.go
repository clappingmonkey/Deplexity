package export

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// MarkdownExporter writes data as Markdown files.
type MarkdownExporter struct {
	OutputDir string
}

// ExportThread writes a single thread as a formatted Markdown file.
func (e *MarkdownExporter) ExportThread(thread *models.Thread) error {
	dir := e.threadDir(thread)
	return writeThreadMarkdown(dir, thread)
}

// writeThreadMarkdown writes a thread as Markdown into the given directory.
func writeThreadMarkdown(dir string, thread *models.Thread) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create thread directory: %w", err)
	}

	var sb strings.Builder

	// Title
	title := thread.Title
	if title == "" {
		title = "Untitled Thread"
	}
	sb.WriteString(fmt.Sprintf("# %s\n\n", markdownText(title)))

	// Metadata
	sb.WriteString(fmt.Sprintf("- **Thread ID:** %s\n", thread.UUID))
	if !thread.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- **Created:** %s\n", thread.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
	}
	if !thread.UpdatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- **Updated:** %s\n", thread.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
	}
	if thread.SpaceUUID != "" {
		sb.WriteString(fmt.Sprintf("- **Space:** %s\n", thread.SpaceUUID))
	}
	sb.WriteString("\n---\n\n")

	// Entries (Q&A pairs)
	for i, entry := range thread.Entries {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}

		// Question
		sb.WriteString(fmt.Sprintf("## Q: %s\n\n", entry.Query))

		// Answer
		sb.WriteString(fmt.Sprintf("%s\n\n", entry.Answer))

		// Sources
		if len(entry.Sources) > 0 {
			sb.WriteString("### Sources\n\n")
			for j, source := range entry.Sources {
				title := source.Title
				if title == "" {
					title = source.URL
				}
				sb.WriteString(fmt.Sprintf("%d. %s", j+1, markdownLink(title, source.URL)))
				if source.Snippet != "" {
					sb.WriteString(fmt.Sprintf(" — %s", markdownText(source.Snippet)))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}

		// Model info
		if entry.Model != "" {
			sb.WriteString(fmt.Sprintf("*Model: %s*\n\n", entry.Model))
		}
	}

	path := filepath.Join(dir, "thread.md")
	if err := writeBytes(path, []byte(sb.String())); err != nil {
		return fmt.Errorf("could not write markdown file: %w", err)
	}

	return nil
}

// ExportSpaces writes a summary Markdown file for all spaces and copies thread markdown into each space folder.
func (e *MarkdownExporter) ExportSpaces(ctx context.Context, spaces []models.Space, threads []models.Thread) error {
	dir := filepath.Join(e.OutputDir, "spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create spaces directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Perplexity Spaces\n\n")
	spaceDirs := spaceDirNames(spaces)

	for spaceIndex, space := range spaces {
		sb.WriteString(fmt.Sprintf("## %s\n\n", markdownText(space.Name)))
		if space.Description != "" {
			sb.WriteString(fmt.Sprintf("%s\n\n", space.Description))
		}
		if space.Instructions != "" {
			sb.WriteString("**AI Instructions:**\n\n")
			sb.WriteString(blockquote(space.Instructions))
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("- **UUID:** %s\n", space.UUID))
		if space.CreatedAt != nil {
			sb.WriteString(fmt.Sprintf("- **Created:** %s\n", space.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
		}
		if len(space.ThreadUUIDs) > 0 {
			sb.WriteString(fmt.Sprintf("- **Threads:** %d\n", len(space.ThreadUUIDs)))
		}

		if len(space.SuggestedQueries) > 0 {
			sb.WriteString("\n**Suggested Queries:**\n\n")
			for _, q := range space.SuggestedQueries {
				sb.WriteString(fmt.Sprintf("- %s\n", q))
			}
		}

		if len(space.Primers) > 0 {
			sb.WriteString("\n**Primers:**\n\n")
			for _, p := range space.Primers {
				sb.WriteString(fmt.Sprintf("- *%s*\n", p.PrimerType))
				for _, q := range p.Queries {
					sb.WriteString(fmt.Sprintf("  - %s\n", q))
				}
			}
		}

		if len(space.Skills) > 0 {
			sb.WriteString("\n**Skills:**\n\n")
			filenames := skillFilenames(space.Skills)
			for i, sk := range space.Skills {
				if sk.Body != "" {
					// Link relative to spaces.md, which lives in the spaces dir.
					// path.Join (forward slashes) keeps the Markdown link valid
					// on every OS, unlike filepath.Join on Windows.
					link := path.Join(spaceDirs[spaceIndex], "skills", filenames[i])
					sb.WriteString("- " + markdownLink(sk.Name, link))
				} else {
					sb.WriteString(fmt.Sprintf("- %s", markdownText(sk.Name)))
				}
				if sk.Description != "" {
					sb.WriteString(fmt.Sprintf(" — %s", sk.Description))
				}
				sb.WriteString("\n")
			}
		}

		sb.WriteString("\n---\n\n")
	}

	spacesMD := filepath.Join(dir, "spaces.md")
	if err := writeBytes(spacesMD, []byte(sb.String())); err != nil {
		return fmt.Errorf("could not write spaces markdown: %w", err)
	}

	// Copy thread markdown into each space folder.
	threadByUUID := make(map[string]*models.Thread, len(threads))
	for i := range threads {
		threadByUUID[threads[i].UUID] = &threads[i]
	}
	for i, space := range spaces {
		spaceDir := filepath.Join(dir, spaceDirs[i])

		// Write skill bodies as SKILL.md sidecar files.
		if err := writeSpaceSkillBodies(spaceDir, space.Skills); err != nil {
			return err
		}

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
			threadDir := filepath.Join(spaceDir, "threads", threadDirName(thread.Slug, thread.UUID))
			if err := writeThreadMarkdown(threadDir, thread); err != nil {
				return err
			}
		}
	}

	return nil
}

// writeSpaceSkillBodies writes each skill's SKILL.md body into a skills/
// subfolder of baseDir (a space or the account directory). Skills without a
// fetched body are skipped. Filenames are collision-free across the slice and
// agree with the links written by the caller.
func writeSpaceSkillBodies(baseDir string, skills []models.Skill) error {
	filenames := skillFilenames(skills)
	var skillsDir string
	for i := range skills {
		if skills[i].Body == "" {
			continue
		}
		if skillsDir == "" {
			skillsDir = filepath.Join(baseDir, "skills")
			if err := os.MkdirAll(skillsDir, 0755); err != nil {
				return fmt.Errorf("could not create skills directory: %w", err)
			}
		}
		filename := filenames[i]
		if err := writeBytes(filepath.Join(skillsDir, filename), []byte(skills[i].Body)); err != nil {
			return fmt.Errorf("could not write skill body %s: %w", filename, err)
		}
	}
	return nil
}

// ExportUser writes the user profile as Markdown.
func (e *MarkdownExporter) ExportUser(user *models.User) error {
	dir := filepath.Join(e.OutputDir, "profile")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create profile directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Perplexity Profile\n\n")
	sb.WriteString(fmt.Sprintf("- **Name:** %s\n", markdownText(user.Name)))
	sb.WriteString(fmt.Sprintf("- **Email:** %s\n", user.Email))
	if user.Subscription != "" {
		sb.WriteString(fmt.Sprintf("- **Subscription:** %s\n", user.Subscription))
	}
	sb.WriteString(fmt.Sprintf("- **User ID:** %s\n", user.ID))

	path := filepath.Join(dir, "user.md")
	if err := writeBytes(path, []byte(sb.String())); err != nil {
		return fmt.Errorf("could not write profile markdown: %w", err)
	}

	return nil
}

// ExportAccount writes account-wide data (currently global skills) as Markdown.
//
// Global skills apply to every request regardless of space, so they are written
// once under account/ rather than per space. Bodies go to account/skills/ and
// account/global-skills.md links to them.
func (e *MarkdownExporter) ExportAccount(account *models.Account) error {
	if account == nil {
		return nil
	}
	dir := filepath.Join(e.OutputDir, "account")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create account directory: %w", err)
	}

	// Write skill bodies first so the links below point at real files.
	if err := writeSpaceSkillBodies(dir, account.GlobalSkills); err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("# Global Skills\n\n")
	sb.WriteString("Account-wide skills applied to every request, regardless of space.\n\n")

	if len(account.GlobalSkills) == 0 {
		sb.WriteString("_No global skills._\n")
	} else {
		filenames := skillFilenames(account.GlobalSkills)
		for i, sk := range account.GlobalSkills {
			if sk.Body != "" {
				// Link relative to global-skills.md, which lives in the account
				// dir. path.Join keeps forward slashes for portable Markdown.
				link := path.Join("skills", filenames[i])
				sb.WriteString("- " + markdownLink(sk.Name, link))
			} else {
				sb.WriteString(fmt.Sprintf("- %s", markdownText(sk.Name)))
			}
			if sk.Description != "" {
				sb.WriteString(fmt.Sprintf(" — %s", sk.Description))
			}
			sb.WriteString("\n")
		}
	}

	out := filepath.Join(dir, "global-skills.md")
	if err := writeBytes(out, []byte(sb.String())); err != nil {
		return fmt.Errorf("could not write global skills markdown: %w", err)
	}
	return nil
}

// threadDir returns the output directory for a thread.
func (e *MarkdownExporter) threadDir(thread *models.Thread) string {
	return filepath.Join(e.OutputDir, "threads", threadDirName(thread.Slug, thread.UUID))
}

// blockquote renders text as a Markdown blockquote, prefixing every line with
// "> " so multi-line instructions render correctly instead of collapsing into
// a single quoted line. The result ends with a trailing newline.
func blockquote(text string) string {
	var sb strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			sb.WriteString(">\n")
			continue
		}
		sb.WriteString("> ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// markdownText escapes plain text, such as a title or a name, for inline
// Markdown: it cannot start a link, code span, emphasis, HTML tag or entity,
// and stays on one line. Answers, queries and descriptions are Markdown as
// written and are not passed through it.
func markdownText(s string) string {
	return markdownTextEscaper.Replace(s)
}

// markdownLink renders a Markdown link whose text and target cannot break out
// of the link syntax. The target is checked before it is written, and written
// so that Markdown reads back exactly the checked URL: backslashes, spaces and
// angle brackets are percent-encoded and & is written as &amp;, so neither a
// backslash escape nor an entity such as &#58; can change it. Targets with
// parentheses are wrapped in <...>. Targets that do
// not parse, or whose scheme is not http(s) or mailto (javascript:, data:,
// ...), are shown as plain text instead.
func markdownLink(text, target string) string {
	text = markdownText(text)
	u, err := url.Parse(target)
	if err != nil || target == "" || !allowedLinkScheme(u.Scheme) {
		return text
	}
	target = markdownLinkTarget.Replace(target)
	if strings.ContainsAny(target, "()") {
		target = "<" + target + ">"
	}
	return "[" + text + "](" + target + ")"
}

var (
	markdownTextEscaper = strings.NewReplacer(
		`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
		"<", `\<`, ">", `\>`, "&", `\&`, "!", `\!`, "|", `\|`, "~", `\~`,
		"\r\n", " ", "\r", " ", "\n", " ",
	)
	markdownLinkTarget = strings.NewReplacer(`\`, "%5C", "<", "%3C", ">", "%3E", " ", "%20", "&", "&amp;")
)

// allowedLinkScheme reports whether a link target with this scheme may be
// written as a link: relative targets, http(s) and mailto, in any case.
func allowedLinkScheme(scheme string) bool {
	return scheme == "" || strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https") || strings.EqualFold(scheme, "mailto")
}
