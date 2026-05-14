# Deplexity

**Export your Perplexity AI conversations, spaces, and profile — fully offline, no API key required.**

Deplexity is a CLI tool that authenticates via your browser session and exports all your Perplexity AI data into portable formats (JSON, Markdown, PDF). Built in pure Go with zero runtime dependencies.

---

## Why Deplexity?

Perplexity AI has no official data export feature. Your research threads, curated spaces, and citation-rich answers are locked inside their platform. Deplexity gives you:

- **Full ownership** of your data in open formats
- **Offline archives** you can search, version-control, or feed into other tools
- **PDF reports** with source tables — no browser or LaTeX needed
- **Incremental backups** via scripting/cron

---

## Installation

### From source (requires Go 1.22+)

```bash
go install github.com/clappingmonkey/deplexity/cmd/deplexity@latest
```

### From source with Bazel

```bash
bazel build //cmd/deplexity
# Binary at: bazel-bin/cmd/deplexity/deplexity_/deplexity
```

### Pre-built binaries

Download from [Releases](https://github.com/clappingmonkey/deplexity/releases) (coming soon).

---

## Quick Start

```bash
# 1. Authenticate (opens browser — log in to Perplexity)
deplexity login

# 2. Export everything
deplexity export

# 3. Check your data
ls deplexity-export/
```

That's it. Your threads, spaces, and profile are now in `./deplexity-export/`.

---

## Usage

### Authentication

```bash
# Interactive browser login (recommended)
deplexity login

# Headless/server: provide session cookie directly
deplexity login --cookie "YOUR_SESSION_TOKEN"

# Check session status
deplexity status

# Remove saved session
deplexity logout
```

The session token is stored at `~/.config/deplexity/session.json` (mode 0600).

### Export

```bash
# Export with defaults (JSON + Markdown)
deplexity export

# Export to a specific directory
deplexity export -o ~/backups/perplexity

# Export as PDF
deplexity export -f pdf

# Export all formats
deplexity export -f json -f markdown -f pdf

# Export only threads (skip spaces/profile)
deplexity export --no-spaces --no-profile

# Adjust rate limiting (milliseconds between requests)
deplexity export --delay 1000
```

### Output Structure

```
deplexity-export/
├── manifest.json              # Export metadata
├── profile/
│   └── user.json
├── spaces/
│   ├── index.json
│   └── my-space/
│       └── space.json
└── threads/
    ├── index.json
    └── my-research-thread/
        ├── thread.json        # Full thread with entries + sources
        ├── thread.md          # Human-readable Markdown
        ├── thread.pdf         # PDF with source tables
        └── sources.json       # All citations extracted
```

---

## Export Formats

| Format   | Best For                                    |
| -------- | ------------------------------------------- |
| JSON     | Programmatic access, backups, data pipelines |
| Markdown | Reading, note-taking apps, version control   |
| PDF      | Sharing, printing, archival                  |

---

## How It Works

1. **Login** — Opens a real Chrome window (or uses a provided cookie). Captures the `__Secure-next-auth.session-token` cookie after you authenticate normally.
2. **Fetch** — Uses Perplexity's internal REST API with your session cookie. Rate-limited by default to avoid triggering anti-abuse.
3. **Export** — Converts raw API responses into clean domain models, then renders them in your chosen format(s).

No credentials are stored — only the session cookie (which expires in ~7 days).

---

## Building

Requires [Bazel 9.1+](https://bazel.build/install):

```bash
# Build
bazel build //cmd/deplexity

# Build with version stamping
bazel build //cmd/deplexity --config=release

# Run tests
bazel test //...

# Regenerate BUILD files after code changes
bazel run //:gazelle
```

Or with plain Go:

```bash
go build -o deplexity ./cmd/deplexity
go test ./...
```

---

## Architecture

```
cmd/deplexity/       CLI entrypoint (Kong framework)
internal/
├── api/             Raw API types + data fetching
├── auth/            Browser login, cookie management, session validation
├── client/          Authenticated HTTP client with rate limiting + retry
├── export/          JSON, Markdown, and PDF renderers
└── models/          Clean domain models (decoupled from API shape)
```

Key design decisions:
- **Pure Go** — Single static binary, no CGO, no external tools
- **Context propagation** — All operations are cancellable (Ctrl+C works instantly)
- **Layered architecture** — API types are separate from domain models; exporters are independent
- **PDF via gpdf** — Declarative grid layout, no browser/wkhtmltopdf/LaTeX dependency

---

## Requirements

- **Go 1.22+** (build only)
- **Chrome/Chromium** (for `deplexity login` only — auto-downloaded if missing)
- No runtime dependencies for `export` or `login --cookie`

---

## Security

- Session tokens are stored with `0600` permissions in `~/.config/deplexity/`
- No credentials are ever logged or transmitted to third parties
- The tool only communicates with `perplexity.ai`
- All HTTP requests use the same headers as a real browser session

---

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Run `bazel test //...` (all tests must pass)
4. Run `bazel run //:gazelle` if you added/removed files
5. Submit a pull request

---

## License

[MIT](LICENSE)

---

## Disclaimer

Deplexity is an independent, community-built tool. It is not affiliated with, endorsed by, or sponsored by Perplexity AI. Use responsibly and in accordance with Perplexity's terms of service.
