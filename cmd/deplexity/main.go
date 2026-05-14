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

	fmt.Printf("Exporting to: %s\n", cmd.Output)
	fmt.Printf("Formats: %v\n\n", cmd.Format)

	var threads []models.Thread
	var spaces []models.Space
	var user *models.User

	if cmd.Threads {
		fmt.Print("Fetching thread list...")
		threads, err = api.ListThreads(ctx, c)
		if err != nil {
			return err
		}
		fmt.Printf(" %d threads found\n", len(threads))
	}

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

	if cmd.Profile {
		fmt.Print("Fetching profile...")
		user, err = api.GetUser(ctx, c)
		if err != nil {
			return err
		}
		fmt.Printf(" %s\n", user.Email)
	}

	// Fetch full thread details
	if cmd.Threads && len(threads) > 0 {
		fmt.Println()
		bar := progressbar.NewOptions(len(threads),
			progressbar.OptionSetDescription("Fetching thread details"),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWidth(40),
		)

		for i, t := range threads {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			full, err := api.GetThread(ctx, c, t.UUID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n  Warning: could not fetch thread %s: %v\n", t.UUID, err)
				continue
			}
			threads[i] = *full
			_ = bar.Add(1)
		}
		fmt.Println()
	}

	// Export in each format
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

	jsonExp := &export.JSONExporter{OutputDir: cmd.Output}
	if err := jsonExp.ExportManifest(manifest); err != nil {
		return err
	}

	fmt.Printf("\nExport complete: %s\n", cmd.Output)
	return nil
}

func (cmd *ExportCmd) exportFormat(_ context.Context, format string, threads []models.Thread, spaces []models.Space, user *models.User) error {
	switch format {
	case "json":
		exp := &export.JSONExporter{OutputDir: cmd.Output}
		if len(threads) > 0 {
			if err := exp.ExportThreadIndex(threads); err != nil {
				return err
			}
			for i := range threads {
				if err := exp.ExportThread(&threads[i]); err != nil {
					return err
				}
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
