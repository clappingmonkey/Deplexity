# Deplexity

**Export your Perplexity AI conversations, spaces, and profile — fully offline, no API key required.**

Deplexity is a CLI tool that authenticates via your browser session and exports all your Perplexity AI data into portable formats (JSON, Markdown, PDF). Built in pure Go with zero runtime dependencies.

<p align="center">
  <img src="demo.gif" alt="Deplexity demo — exporting Perplexity threads to JSON, Markdown, and PDF" width="800">
</p>

---

## Why Deplexity?

Perplexity AI has no official data export feature. Your research threads, curated spaces, and citation-rich answers are locked inside their platform. Deplexity gives you:

- **Full ownership** of your data in open formats
- **Offline archives** you can search, version-control, or feed into other tools
- **PDF reports** with source attribution — no browser or LaTeX needed
- **Incremental backups** — resumable exports pick up where they left off

---

## Installation

### From source (requires Go 1.26+)

```bash
go install github.com/clappingmonkey/deplexity/cmd/deplexity@latest
```

### From source with Bazel

```bash
bazel build //cmd/deplexity
# Binary at: bazel-bin/cmd/deplexity/deplexity_/deplexity
```

### Pre-built binaries

Download from [Releases](https://github.com/clappingmonkey/deplexity/releases).

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

# Re-fetch thread index even if a cached one exists
deplexity export --refresh

# Verbose output (show API calls and timing)
deplexity export -v

# Export only threads (skip spaces/profile)
deplexity export --no-spaces --no-profile

# Adjust rate limiting (milliseconds between requests)
deplexity export --delay 1000

# Control PDF parallelism (default: auto-detect CPU count)
deplexity export -f pdf --pdf-workers 4
```

### Resumable Exports

Export runs in two phases:

1. **Phase 1 — Index**: Fetches the list of all threads and caches it in `thread_index.json`. If interrupted, re-running `deplexity export` resumes from the cached index.
2. **Phase 2 — Details**: Fetches full content for each thread. Already-fetched threads are skipped automatically.

Use `--refresh` to force re-fetching the thread index (e.g., after new conversations).

### Output Structure

```
deplexity-export/
├── manifest.json              # Export metadata (timestamp, counts, version)
├── thread_index.json          # Cached thread list (for resumable exports)
├── profile/
│   └── user.json
├── spaces/
│   ├── index.json
│   ├── spaces.md
│   └── <space-name>/
│       ├── space.json         # Incl. AI instructions, suggested queries, primers, skills metadata
│       ├── skills/            # Attached skills' SKILL.md bodies (referenced by space.json)
│       │   └── <skill-name>.md
│       └── threads/           # Self-contained copies of this space's threads
│           └── <thread-slug>/
│               ├── thread.json
│               ├── thread.md
│               ├── thread.pdf
│               └── sources.json
└── threads/                   # Canonical flat list of all threads
    └── <thread-slug>/
        ├── thread.json
        ├── thread.md
        ├── thread.pdf
        └── sources.json
```

Each space folder is self-contained — you can ZIP and share a single space without needing the top-level `threads/` directory.

Space exports capture each space's full context: its custom AI instructions, description, suggested queries, primers, and any attached skills. Skill definitions are written as `SKILL.md` files under `spaces/<space-name>/skills/` and referenced from `space.json`.

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
2. **Fetch** — Uses Perplexity's internal REST API with your session cookie. TLS fingerprinting matches a real Chrome browser via [utls](https://github.com/refraction-networking/utls). Adaptive rate limiting keeps requests under Perplexity's anti-abuse threshold (~2 req/s).
3. **Export** — Converts raw API responses into clean domain models, then renders them in your chosen format(s). Exports are resumable — interrupted runs pick up where they left off.

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
├── client/          HTTP client: utls transport, adaptive rate limiting, retry with backoff
├── export/          JSON, Markdown, and PDF renderers
└── models/          Clean domain models (decoupled from API shape)
```

Key design decisions:
- **Pure Go** — Single static binary, no CGO, no external tools
- **Context propagation** — All operations are cancellable (first Ctrl+C cancels gracefully, second force-quits)
- **Layered architecture** — API types are separate from domain models; exporters are independent
- **Cloudflare bypass** — Chrome TLS fingerprint via `refraction-networking/utls`
- **Adaptive rate limiting** — Delay doubles after 429s, halves after sustained success
- **Two-phase export** — Thread index cached separately; detail fetching skips completed threads
- **PDF via gpdf** — Declarative grid layout, no browser/wkhtmltopdf/LaTeX dependency

---

## Requirements

- **Go 1.26+** (build only)
- **Chrome/Chromium** (for `deplexity login` only — auto-downloaded if missing)
- No runtime dependencies for `export` or `login --cookie`

---

## Security

- Session tokens are stored with `0600` permissions in `~/.config/deplexity/`
- No credentials are ever logged or transmitted to third parties
- Requests to Perplexity use the same TLS fingerprint and headers as a real Chrome session
- Skill bodies are downloaded from the pre-signed storage URLs (Amazon S3) that Perplexity returns; these fetches deliberately carry **no** session cookie or Perplexity headers, so your credentials are never sent off-platform

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
