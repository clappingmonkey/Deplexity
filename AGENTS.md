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

- `internal/api/types.go` — **raw API response structs** (JSON tags match Perplexity's undocumented internal API, verified against live responses May 2026, API v2.18).
- `internal/api/threads.go` — `ListThreads`, `ListThreadsFrom` (POST `list_ask_threads` with pagination, dual stop condition), `GetThread`.
- `internal/api/collections.go` — `ListCollections` via `GET /rest/spaces`.
- `internal/api/user.go` — `GetUser` via `GET /api/user`.
- `internal/models/models.go` — clean domain models used throughout the app (decoupled from API shape).
- `internal/auth/` — browser-based login via `go-rod/rod` (visible Chrome), cookie capture, session persistence. Also supports `--cookie` for manual token auth.
- `internal/client/client.go` — authenticated `net/http` client with raw `Cookie` header, adaptive rate limiting, separate HTTP (429/5xx) and network (DNS/dial/TLS) retry loops. All methods accept `context.Context`.
- `internal/client/transport.go` — Chrome TLS fingerprint via `refraction-networking/utls` to bypass Cloudflare.
- `internal/client/ratelimit.go` — `RetryWithBackoff`, `computeBackoff`, shared retry constants.
- `internal/export/` — JSON, Markdown, and PDF exporters. PDF uses `gpdf` (pure Go, zero dependencies). JSON exporter handles `thread_index.json` persistence for resumable exports. All exporters copy thread files into space folders for self-contained output.
- `internal/export/util.go` — shared helpers: `sanitizeFilename`, `threadSlug`.
- CLI framework: `alecthomas/kong` (struct-tag based). Commands defined as types with `Run(ctx context.Context) error` methods in `main.go`.

## Browser Dependency

**For `login` only:** Chrome or Chromium is needed for browser-based authentication. If not found, Rod automatically downloads Chromium (~80MB, one-time, cached at `~/.cache/rod/`).

**For `export --cookie` and PDF:** No browser required at any point. `login --cookie <TOKEN>` bypasses the browser entirely. PDF export uses `gpdf` (pure Go, no CGO, no browser subprocess).

## Perplexity API Details

All endpoints are reverse-engineered from browser DevTools (May 2026, API version 2.18).

- Auth cookie: `__Secure-next-auth.session-token` (NextAuth.js, ~7 day expiry)
- Session stored at: `~/.config/deplexity/session.json` (mode 0600, plain JSON)
- Required headers: `User-Agent` (Chrome), `Referer`, `Origin`, `x-app-apiclient: default`, `x-app-apiversion: 2.18`
- Cloudflare TLS fingerprinting bypassed via `utls` Chrome preset

### Endpoints Used

| Method | Endpoint | Purpose |
|--------|----------|---------|
| POST | `/rest/thread/list_ask_threads` | All threads with pagination (body: `{limit, offset, ascending, ...}`) |
| GET | `/rest/thread/{uuid}?with_schematized_response=true` | Thread detail with full entries |
| GET | `/rest/spaces` | All spaces (private/shared/invited/org/saved) |
| GET | `/api/user` | User profile |
| GET | `/api/auth/session` | Session info + expiry |
| GET | `/rest/user/settings` | Limits, quotas, connector config |
| GET | `/rest/rate-limit/all` | Rate limits per model |
| POST | `/rest/files/list` | Uploaded files (not yet implemented) |

### Thread Content Structure

Answer content is in `entries[].blocks[]` where `intended_usage == "ask_text_0_markdown"` → `markdown_block.answer`.

## Export Flow

1. **Phase 1 — Index**: `POST /rest/thread/list_ask_threads` with pagination (limit=20, ascending=false). Dual stop condition: `len(response) < limit` (primary) + all-duplicates (safety net). Result cached in `thread_index.json` with `Complete: true` flag.
2. **Phase 2 — Details**: `GET /rest/thread/{uuid}` for each thread. Skips threads already fetched on disk. Adaptive rate limiting: delay doubles after 429, halves after 20 consecutive successes.
3. **Phase 3 — Render**: Convert domain models to JSON/Markdown/PDF. Write threads to `threads/<slug>/` (canonical flat list). Copy thread files into `spaces/<name>/threads/<slug>/` so each space folder is self-contained.

Use `--refresh` to force re-fetching the thread index.

## Gotchas

- `has_next_page` from the API is always `true` even on the last page — useless, ignored.
- `total_threads` reports incorrect counts (e.g., 99 when actual is 692) — unreliable, ignored.
- Collection/space info is embedded inline in each thread from `list_ask_threads`; there is no server-side filter by space.
- Cloudflare blocks HTML page fetching even with valid cookies, but `/rest/` API endpoints work fine.
- `deplexity login` must be run before `export` — there is no inline auth flow. Alternatively, use `deplexity login --cookie <TOKEN>` for headless/server environments.
- Raw `Cookie` header is used instead of `http.CookieJar`/`AddCookie` to avoid Go's cookie domain validation issues.
- Signal handling: first Ctrl+C cancels the context (graceful stop after current operation), second Ctrl+C restores default behavior and force-exits. Implemented via `signal.NotifyContext` + goroutine that calls `cancel()` to restore defaults.

## Bazel

- Bazel 9.1+ required (see `.bazelversion`)
- Uses bzlmod exclusively (no WORKSPACE file)
- `rules_go` 0.60.0, `gazelle` 0.42.0
- Run `bazel run //:gazelle` after adding/removing Go files
- Stamping: `bazel build //cmd/deplexity --config=release` injects git tag + build timestamp
- Cross-compilation platforms defined in `build/platforms/BUILD.bazel`
