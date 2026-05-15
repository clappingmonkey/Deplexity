// Package main is the entrypoint for the deplexity CLI tool.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/alecthomas/kong"

	"github.com/clappingmonkey/deplexity/internal/api"
	"github.com/clappingmonkey/deplexity/internal/auth"
	"github.com/clappingmonkey/deplexity/internal/client"
	"github.com/clappingmonkey/deplexity/internal/export"
	"github.com/clappingmonkey/deplexity/internal/models"
	"github.com/schollz/progressbar/v3"
)

// Stamped at build time via -ldflags.
var (
	version   = "dev"
	buildTime = "unknown"
)

// CLI defines the top-level command structure.
type CLI struct {
	Verbose bool       `short:"v" help:"Enable verbose/debug output."`
	Login   LoginCmd   `cmd:"" help:"Authenticate with Perplexity (opens browser or accepts --cookie)."`
	Export  ExportCmd  `cmd:"" help:"Export threads, spaces, and profile data."`
	Status  StatusCmd  `cmd:"" help:"Show current session status and rate limits."`
	Logout  LogoutCmd  `cmd:"" help:"Remove saved session data."`
	Version VersionCmd `cmd:"" help:"Print version information."`
}

// LoginCmd handles authentication.
type LoginCmd struct {
	Cookie string `help:"Provide session token directly (bypasses browser login)." placeholder:"TOKEN"`
}

func (cmd *LoginCmd) Run(ctx context.Context) error {
	var session *models.SavedSession
	var err error

	if cmd.Cookie != "" {
		session, err = auth.CookieLogin(ctx, cmd.Cookie)
	} else {
		session, err = auth.BrowserLogin()
	}
	if err != nil {
		return err
	}

	if err := auth.SaveSession(session); err != nil {
		return fmt.Errorf("could not save session: %w", err)
	}

	path, _ := auth.SessionPath()
	fmt.Printf("Session saved to: %s\n", path)
	return nil
}

// ExportCmd handles data export.
type ExportCmd struct {
	Output  string   `short:"o" default:"deplexity-export" help:"Output directory."`
	Format  []string `short:"f" default:"json,markdown" help:"Export formats (json, markdown, pdf)." enum:"json,markdown,pdf"`
	Threads bool     `help:"Export threads." default:"true" negatable:""`
	Spaces  bool     `help:"Export spaces/collections." default:"true" negatable:""`
	Profile bool     `help:"Export user profile." default:"true" negatable:""`
	Delay   int      `help:"Delay between API requests in milliseconds." default:"500"`
	Refresh bool     `help:"Force re-fetch of thread list even if cached." default:"false"`

	verbose bool // set by main before Run
}

func (cmd *ExportCmd) Run(ctx context.Context) error {
	session, err := auth.LoadSession()
	if err != nil {
		return err
	}

	c, err := client.New(session)
	if err != nil {
		return err
	}
	c.SetDelay(time.Duration(cmd.Delay) * time.Millisecond)
	c.SetVerbose(cmd.verbose)

	jsonExp := &export.JSONExporter{OutputDir: cmd.Output}

	fmt.Printf("Exporting to: %s\n", cmd.Output)
	fmt.Printf("Formats: %v\n\n", cmd.Format)

	// === Phase 1: Thread index (list all UUIDs) ===
	var threadRefs []models.ThreadRef
	if cmd.Threads {
		threadRefs, err = cmd.fetchThreadIndex(ctx, c, jsonExp)
		if err != nil {
			return err
		}
	}

	// === Phase 2: Fetch thread details (resumable) ===
	var threads []models.Thread
	if cmd.Threads && len(threadRefs) > 0 {
		threads, err = cmd.fetchThreadDetails(ctx, c, jsonExp, threadRefs)
		if err != nil {
			return err
		}
	}

	// === Spaces ===
	var spaces []models.Space
	if cmd.Spaces {
		fmt.Print("Fetching spaces...")
		spaces, err = api.ListCollections(ctx, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, " skipped (%v)\n", err)
			spaces = nil
		} else {
			fmt.Printf(" %d spaces found\n", len(spaces))
		}
	}

	// === Profile ===
	var user *models.User
	if cmd.Profile {
		fmt.Print("Fetching profile...")
		user, err = api.GetUser(ctx, c)
		if err != nil {
			return err
		}
		fmt.Printf(" %s\n", user.Email)
	}

	// === Phase 3: Format export ===
	for _, format := range cmd.Format {
		if err := cmd.exportFormat(ctx, format, threads, spaces, user); err != nil {
			return fmt.Errorf("export %s failed: %w", format, err)
		}
	}

	// Write manifest
	manifest := &models.ExportManifest{
		Version:    version,
		ExportedAt: time.Now().UTC(),
		Formats:    cmd.Format,
		Counts: models.ExportCounts{
			Threads: len(threads),
			Spaces:  len(spaces),
		},
		ThreadIndex: make(map[string]string),
	}
	for _, t := range threads {
		manifest.ThreadIndex[t.UUID] = t.Slug
		for _, e := range t.Entries {
			manifest.Counts.Sources += len(e.Sources)
		}
	}

	if err := jsonExp.ExportManifest(manifest); err != nil {
		return err
	}

	fmt.Printf("\nExport complete: %s\n", cmd.Output)
	return nil
}

// fetchThreadIndex loads or fetches the thread UUID list.
func (cmd *ExportCmd) fetchThreadIndex(ctx context.Context, c *client.Client, jsonExp *export.JSONExporter) ([]models.ThreadRef, error) {
	// Check cache
	if !cmd.Refresh {
		index, err := jsonExp.LoadThreadIndex()
		if err != nil {
			return nil, err
		}
		if index != nil && time.Since(index.FetchedAt) < 24*time.Hour {
			fmt.Printf("Using cached thread list (%d threads, fetched %s ago)\n", index.Total, time.Since(index.FetchedAt).Round(time.Minute))
			return index.Threads, nil
		}
	}

	// Fetch from API
	fmt.Print("Fetching thread list...")
	threads, err := api.ListThreads(ctx, c, func(n int) {
		fmt.Printf("\rFetching thread list... %d", n)
	})
	if err != nil {
		return nil, err
	}
	fmt.Printf("\rFetching thread list... %d threads found\n", len(threads))

	// Save to disk
	if err := jsonExp.SaveThreadIndex(threads); err != nil {
		return nil, err
	}

	refs := make([]models.ThreadRef, 0, len(threads))
	for _, t := range threads {
		refs = append(refs, models.ThreadRef{UUID: t.UUID, Title: t.Title})
	}
	return refs, nil
}

// fetchThreadDetails fetches full details for threads not yet on disk.
func (cmd *ExportCmd) fetchThreadDetails(ctx context.Context, c *client.Client, jsonExp *export.JSONExporter, refs []models.ThreadRef) ([]models.Thread, error) {
	// Determine which threads need fetching
	var missing []models.ThreadRef
	for _, ref := range refs {
		if !jsonExp.ThreadDetailExists(ref.UUID) {
			missing = append(missing, ref)
		}
	}

	if len(missing) > 0 {
		fmt.Printf("Fetching thread details: %d/%d remaining\n", len(missing), len(refs))
		bar := progressbar.NewOptions(len(missing),
			progressbar.OptionSetDescription("Fetching details"),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWidth(40),
		)

		for _, ref := range missing {
			select {
			case <-ctx.Done():
				fmt.Printf("\nInterrupted. Progress saved — resume by running the same command.\n")
				return nil, ctx.Err()
			default:
			}

			full, err := api.GetThread(ctx, c, ref.UUID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n  Warning: could not fetch thread %s: %v\n", ref.UUID, err)
				_ = bar.Add(1)
				continue
			}

			// Write to disk immediately
			if err := jsonExp.ExportThread(full); err != nil {
				fmt.Fprintf(os.Stderr, "\n  Warning: could not write thread %s: %v\n", ref.UUID, err)
			}
			_ = bar.Add(1)
		}
		fmt.Println()
	} else {
		fmt.Printf("All %d threads already fetched, skipping detail fetch\n", len(refs))
	}

	// Load all threads from disk
	threads := make([]models.Thread, 0, len(refs))
	for _, ref := range refs {
		t, err := jsonExp.LoadThread(ref.UUID)
		if err != nil {
			continue // skip threads that failed to fetch
		}
		threads = append(threads, *t)
	}

	return threads, nil
}

func (cmd *ExportCmd) exportFormat(_ context.Context, format string, threads []models.Thread, spaces []models.Space, user *models.User) error {
	switch format {
	case "json":
		exp := &export.JSONExporter{OutputDir: cmd.Output}
		// Thread JSONs are already written during Phase 2; just write the rich index.
		if len(threads) > 0 {
			if err := exp.ExportThreadIndex(threads); err != nil {
				return err
			}
		}
		if len(spaces) > 0 {
			if err := exp.ExportSpaces(spaces); err != nil {
				return err
			}
		}
		if user != nil {
			if err := exp.ExportUser(user); err != nil {
				return err
			}
		}

	case "markdown":
		exp := &export.MarkdownExporter{OutputDir: cmd.Output}
		for i := range threads {
			if err := exp.ExportThread(&threads[i]); err != nil {
				return err
			}
		}
		if len(spaces) > 0 {
			if err := exp.ExportSpaces(spaces); err != nil {
				return err
			}
		}
		if user != nil {
			if err := exp.ExportUser(user); err != nil {
				return err
			}
		}

	case "pdf":
		exp := export.NewPDFExporter(cmd.Output)
		defer exp.Close()
		for i := range threads {
			if err := exp.ExportThread(&threads[i]); err != nil {
				return err
			}
		}
	}

	return nil
}

// StatusCmd shows current session info.
type StatusCmd struct{}

func (cmd *StatusCmd) Run(ctx context.Context) error {
	session, err := auth.LoadSession()
	if err != nil {
		return err
	}

	info, err := auth.ValidateSession(ctx, session)
	if err != nil {
		return err
	}

	if !info.Valid {
		fmt.Println("Session: INVALID (expired or revoked)")
		fmt.Println("Run 'deplexity login' to re-authenticate.")
		return nil
	}

	fmt.Printf("Session: VALID\n")
	fmt.Printf("Email:   %s\n", info.Email)
	if !info.ExpiresAt.IsZero() {
		fmt.Printf("Expires: %s\n", info.ExpiresAt.Format("2006-01-02 15:04 UTC"))
	}

	return nil
}

// LogoutCmd removes the session.
type LogoutCmd struct{}

func (cmd *LogoutCmd) Run(_ context.Context) error {
	if !auth.SessionExists() {
		fmt.Println("No session found — already logged out.")
		return nil
	}
	if err := auth.DeleteSession(); err != nil {
		return err
	}
	fmt.Println("Session removed.")
	return nil
}

// VersionCmd prints version info.
type VersionCmd struct{}

func (cmd *VersionCmd) Run(_ context.Context) error {
	fmt.Printf("deplexity %s (built %s)\n", version, buildTime)
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("deplexity"),
		kong.Description("Export your Perplexity AI threads, spaces, and profile."),
		kong.UsageOnError(),
		kong.Vars{"version": version},
		kong.BindTo(ctx, (*context.Context)(nil)),
	)

	// Propagate verbose flag to subcommands that need it.
	cli.Export.verbose = cli.Verbose

	err := kctx.Run()
	kctx.FatalIfErrorf(err)
}
