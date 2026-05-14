# AGENTS.md

## Build & Run

```bash
# Build
bazel build //cmd/deplexity

# Build with version stamping
bazel build //cmd/deplexity --config=release

# Run tests
bazel test //...

# Run a specific test
bazel test //internal/client:client_test

# Run the binary directly
bazel run //cmd/deplexity -- export --help

# Regenerate BUILD files after adding new files/deps
bazel run //:gazelle

# Update deps after editing go.mod
go mod tidy
bazel run //:gazelle-update-repos
bazel run //:gazelle
```

Binary entrypoint: `cmd/deplexity/main.go`. Version/buildTime stamped via `x_defs` in Bazel (or `-ldflags` for plain `go build`).

## Architecture

- `internal/api/types.go` — **raw API response structs** (JSON tags match Perplexity's undocumented internal API). These are reverse-engineered and may need adjustment when tested against real responses.
- `internal/models/models.go` — clean domain models used throughout the app (decoupled from API shape).
- `internal/auth/` — browser-based login via `go-rod/rod` (visible Chrome), cookie capture, session persistence. Also supports `--cookie` for manual token auth.
- `internal/client/` — authenticated `net/http` client with cookie jar, rate limiting, retry with backoff. All methods accept `context.Context` for cancellation.
- `internal/export/` — JSON, Markdown, and PDF exporters. PDF uses `gpdf` (pure Go, zero dependencies, no browser needed). JSON and Markdown are string-based, no template engine.
- CLI framework: `alecthomas/kong` (struct-tag based). Commands defined as types with `Run(ctx context.Context) error` methods in `main.go`.

## Browser Dependency

**For `login` only:** Chrome or Chromium is needed for browser-based authentication. If not found, Rod automatically downloads Chromium (~80MB, one-time, cached at `~/.cache/rod/`).

**For `export --cookie` and PDF:** No browser required at any point. `login --cookie <TOKEN>` bypasses the browser entirely. PDF export uses `gpdf` (pure Go, no CGO, no browser subprocess).

## Perplexity API Details

- Auth cookie: `__Secure-next-auth.session-token` (NextAuth.js, ~7 day expiry)
- Session stored at: `~/.config/deplexity/session.json` (mode 0600, plain JSON)
- Internal endpoints used: `/rest/threads/`, `/rest/threads/{uuid}`, `/rest/collections/`, `/rest/user/`, `/rest/rate-limit/all`, `/api/auth/session`
- Required headers for API calls: `User-Agent` (Chrome), `Referer`, `Origin` (all set in `client.setHeaders`)
- Perplexity uses Cloudflare + TLS fingerprinting — but since requests use real Chrome cookies from an actual browser session, this is not an issue for the HTTP client.

## Gotchas

- `internal/api/types.go` JSON field names (`url_slug`, `query_str`, `web_results`, `collection_uuid`, `is_bookmarked`) are based on reverse-engineering research, not verified against live API. First real test may require field name corrections.
- `deplexity login` must be run before `export` — there is no inline auth flow. Alternatively, use `deplexity login --cookie <TOKEN>` for headless/server environments.

## Bazel

- Bazel 9.1+ required (see `.bazelversion`)
- Uses bzlmod exclusively (no WORKSPACE file)
- `rules_go` 0.60.0, `gazelle` 0.42.0
- Run `bazel run //:gazelle` after adding/removing Go files
- Stamping: `bazel build //cmd/deplexity --config=release` injects git tag + build timestamp
