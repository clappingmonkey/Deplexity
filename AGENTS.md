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
- `internal/api/threads.go` — `ListThreads` (POST `list_ask_threads` with pagination and dual stop condition), `GetThread`.
- `internal/api/collections.go` — `ListCollections` via `GET /rest/spaces`, deduplicated by non-empty UUID, then always-on fail-soft enrichment (failures are recorded in an `api.Issues` collector, never silently dropped; cancellation and `client.ErrNotAuthenticated` abort, see `api.IsFatal`): `GetCollection` (per-space instructions/description/suggested_queries/primers) and `ListSpaceSkills`/`GetSkillDetail` (collection-scoped skills + SKILL.md body). `ListSpaceSkills` omits `collection_uuid` when the UUID is empty (used to list account-wide global skills); a repeated cursor or more than `maxSkillPages` pages is an error. Space skills are deduplicated by ID like global skills.
- `internal/api/skills.go` — account-wide skills. `enrichSkill` (shared fail-soft helper: fetches detail + downloads SKILL.md body, used by both space and global paths), `ListGlobalSkills` (calls `ListSpaceSkills("")`, keeps only `scope=="global"`), and `GetAccount` (wraps global skills in `models.Account`). Global skills apply to every request account-wide, so they are exported **once** at the top level, not per space.
- `internal/api/user.go` — `GetUser` via `GET /api/user`.
- `internal/models/models.go` — clean domain models used throughout the app (decoupled from API shape).
- `internal/auth/` — browser-based login via `chromedp` (visible window of an installed Chromium-based browser), cookie capture, session persistence. Also supports `--cookie` for manual token auth. `BrowserLogin(ctx, browserPath)` finds the browser (`--browser`, then `DEPLEXITY_BROWSER`, then known Chrome/Chromium/Edge/Brave locations in `browser_find.go`; an explicit path must exist, a bare name is looked up on `PATH` only, and there is no fallback). The browser is driven by a hidden `__browser-login-helper` subprocess of the Deplexity binary (`browser_helper.go`), because chromedp can still panic in its own goroutines on malformed browser messages and its teardown can block (on macOS `killLeftovers` runs a context-free `ps`). The parent (`superviseLogin`) owns the throwaway `deplexity-login-*` profile (passed to the browser with its parent symlinks resolved), the 5-minute `loginTimeout` and cancellation. It sends one JSON request on the helper's stdin and reads one size-capped JSON result line from stdout (session or error with an error kind). It never signals a bare process ID: on unix the helper runs in its own process group (`procgroup_unix.go`), on Windows in a kill-on-close Job Object (`procgroup_windows.go`) created before the request is sent, so the browser is always inside it. To stop the helper it closes stdin, waits up to `helperExitTimeout`, kills the group/job (only before `Wait` reaps the helper, so the ID cannot be reused), and always removes the profile. The helper kills its browser through the `*os.Process` handle chromedp waits on, never a cached pid. A session that arrives while the helper is being stopped still wins over the cancel or timeout. Browser children that call `setsid` leave the unix group, and chromedp skips its own leftover sweep for a profile it did not create, so both the helper and the parent run `sweepProfileProcesses` (`leftovers_*.go`: kill processes with an exact `--user-data-dir=<profile>` argument in either the given or the symlink-resolved spelling, read from `/proc` on Linux and from sysctl `kern.procargs2` on macOS (no `ps` subprocess), only for this user; argv[0] never counts. Linux opens a pidfd first (a process whose pidfd cannot be opened is skipped, never signalled by number), re-reads argv and signals through the pidfd. On macOS `kern.procargs2` pads the executable path with NULs to the pointer size (8 bytes), and a leading empty argument is indistinguishable from more padding. `parseProcArgs` accepts a buffer only when the NULs are exactly the padding (so no process with an empty argv[0], never a browser process) and it holds exactly `argc` complete arguments; anything else is left alone, so environment entries are never read as arguments. Safety relies on the 8-byte padding of the 64-bit Darwin ABI. Before sweeping, `procArgsLayoutOK` runs a smoke test (parsing this process's own buffer must give exactly `os.Args`) and kills nothing otherwise; it catches a different layout on this system but cannot prove every other process's buffer. A process must also run an executable inside the browser's install directory (outermost `.app`) with argv[0] naming it, and the start time and argv are re-checked right before the kill. The crashpad handler has no `--user-data-dir` and is not swept; it exits once the browser is gone. A no-op on Windows, where the job covers them) before removing the profile. Sweep plus removal run in a goroutine bounded by `profileCleanupTimeout`; a profile that could not be removed is reported (`errProfileNotRemoved`: a warning after a successful login, an error otherwise). The helper accepts only a `deplexity-login-*` profile directly inside the temp dir (both the helper and the parent sweep and remove it, so whichever survives cleans up; the helper does this before closing stdout, because the parent kills it once stdout ends), ignores SIGINT and SIGPIPE, uses `--remote-debugging-pipe` (no debugging port), kills its browser when stdin closes, and has a watchdog. A login stays in the force-quit registry from profile creation until its profile cleanup has finished. Force quit calls `auth.KillLoginHelpers()` (snapshots the registry, then kills and cleans every login concurrently, returning within one 2s `forceCleanupTimeout` even if a helper is stuck starting; a killed login that had not started yet never starts, `errLoginStopped`) and prints any failure without blocking on stderr (`writeBounded`) before exiting, because `os.Exit` skips defers and only Linux has `Pdeathsig`. `waitForSession` stops when the session cookie appears, the login window is closed (no `page` target left; on macOS the process outlives its last window), or 3 cookie reads in a row fail. A browser that dies before the page loads is reported with the tail of its output (`errBrowserExited`). Rod was replaced because it panics in background goroutines that callers cannot recover, ignores ctx in several places, and downloads a 2024 Chromium. Only cookies for `perplexity.ai` or `*.perplexity.ai` are kept; among several session cookies, expired ones are ignored, the latest expiry wins over session-only, and only the chosen one is saved (the client sends every saved cookie in the `Cookie` header). `ExpiresAt` comes from the cookie (browser) or the `/api/auth/session` `expires` when it is in the future (`--cookie`, also stored on the cookie), falling back to 7 days.
- `internal/client/client.go` — authenticated `net/http` client with raw `Cookie` header and adaptive rate limiting. `Get`, `Post` and `GetRawURL` share one retry loop (`do`): retryable statuses (429/502/503/504) and network errors (DNS/dial/TLS) are retried separately. Network-retry exhaustion wraps the last error (`%w`); HTTP-retry exhaustion reports the last status code. Certificate failures, rejected redirects and non-https requests are never retried. Every redirect hop must stay on https (`checkRedirect`, max 10), and the query is stripped from the `Referer` that net/http adds on redirects. Transport errors are displayed only through `redactURLError`, which builds the message from known-safe parts (error type, DNS host, syscall errno, redacted redirect URL) and never shows net/http's raw text, since that embeds full URLs with queries, userinfo and fragments; the original error stays reachable via `errors.Is`/`errors.As`, and the utls handshake has its own 15s deadline. `Close` releases pooled connections. API JSON is decoded with a 128 MB cap and trailing data is rejected; error messages include at most 4 KB of the body. The adaptive delay decays back to the configured `--delay`, not to `DefaultDelay`. All methods accept `context.Context`. `GetRawURL` fetches absolute third-party URLs (pre-signed S3 skill bodies) *without* the session cookie/Origin/`x-app-*` headers. It validates the target is `https` with a non-empty host, caps the body at 10 MB (errors on overflow rather than truncating), does not touch the adaptive delay, and never puts the query string or the response body in errors or logs.
- `internal/client/transport.go` — Chrome TLS fingerprint via `refraction-networking/utls` to bypass Cloudflare. A single pooled `http.Transport` dials through utls and negotiates HTTP/2 or HTTP/1.1 from ALPN. This depends on Go 1.27+, where `net/http` accepts any conn with a `ConnectionState() tls.ConnectionState` method, so `golang.org/x/net/http2` is not used. Non-https requests are refused. Proxies are deliberately unsupported: `net/http` does its own TLS handshake for proxied requests, which would lose the fingerprint.
- `internal/client/ratelimit.go` — `computeBackoff` (exponential with jitter) and shared retry constants.
- `internal/export/` — JSON, Markdown, and PDF exporters. PDF uses `gpdf` (pure Go, zero dependencies). JSON exporter handles `thread_index.json` persistence for resumable exports. All exporters copy thread files into space folders for self-contained output.
- `internal/export/util.go` — shared helpers: `sanitizeFilename`, `threadDirName` (slug-readable, UUID-identity thread directories), `spaceDirNames` (identity-suffixed, collision-free space directories shared by all exporters), and `skillFilenames`/`shortID` (collision-free `.md` filenames for a space's skills, disambiguated by a short skill-ID suffix; used by both the JSON and Markdown exporters so their sidecar paths and links agree).
- `internal/export/pdf_fingerprint.go` — incremental PDF export. `pdfRenderInput` (exactly the values `GenerateThreadPDF` renders), `ThreadPDFFingerprint` (`v1:sha256:` hash of `pdfLayoutVersion` + `gpdf.Version` + the render input), and the `.thread.pdf.fingerprint` sidecar helpers (`PDFUpToDate`, `RemovePDFFingerprint`, `WritePDFFingerprint`).
- CLI framework: `alecthomas/kong` (struct-tag based). Commands defined as types with `Run(ctx context.Context) error` methods in `main.go`.

## Browser Dependency

**For `login` only:** an installed Chrome, Chromium, Edge or Brave is needed for browser-based authentication. Nothing is downloaded; without a browser (or a display on Linux) the error points to `--browser` and `login --cookie`.

**Testing the browser login:** `internal/auth/browser_e2e_test.go` drives a real browser against a fake `www.perplexity.ai` (local TLS server, `--host-resolver-rules` and `--ignore-certificate-errors-spki-list`). It skips unless `DEPLEXITY_TEST_BROWSER` is set, and runs headless unless `DEPLEXITY_TEST_BROWSER_VISIBLE` is set:

```bash
DEPLEXITY_TEST_BROWSER="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" go test -run E2E ./internal/auth/
# Under Bazel the macOS sandbox blocks Chrome's own sandbox, so run it unsandboxed:
bazel test //internal/auth:auth_test --strategy=TestRunner=local \
  --test_env=DEPLEXITY_TEST_BROWSER="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --test_arg=-test.run=E2E
```

**For `export --cookie` and PDF:** No browser required at any point. `login --cookie <TOKEN>` bypasses the browser entirely. PDF export uses `gpdf` (pure Go, no CGO) in isolated helper subprocesses of the Deplexity binary so cancellation and per-thread timeouts can terminate a stuck renderer.

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
| GET | `/rest/spaces` | All spaces (private/shared/invited/org/saved) — list only, omits instructions/skills |
| GET | `/rest/collections/get_collection?collection_slug={slug}` | Per-space detail: instructions, description, suggested_queries, primers (param must be `collection_slug`; `collection_uuid` → 422) |
| GET | `/rest/skills/selectable?collection_uuid={uuid}` | Skills selectable in a space; space-attached skills have `scope=="collection"` (vs `global`) |
| GET | `/rest/skills/selectable` (no `collection_uuid`) | Account-wide skills; returns only `scope=="global"` entries (applied to every request) |
| GET | `/rest/skills/{id}?view_scope=individual` | Skill detail incl. pre-signed S3 `file_url` for the SKILL.md body |
| GET | `/api/user` | User profile |
| GET | `/api/auth/session` | Session info + expiry |
| GET | `/rest/user/settings` | Limits, quotas, connector config |
| GET | `/rest/rate-limit/all` | Rate limits per model |
| POST | `/rest/files/list` | Uploaded files (not yet implemented) |

### Thread Content Structure

Answer content is in `entries[].blocks[]` where `intended_usage == "ask_text_0_markdown"` → `markdown_block.answer`.

## Export Flow

1. **Phase 1 — Index**: `POST /rest/thread/list_ask_threads` with pagination (limit=20, ascending=false). Dual stop condition: `len(response) < limit` (primary) + current-run all-duplicates (safety net). Result cached in `thread_index.json` with `Complete: true` flag, including each original thread slug. An incomplete index is never resumed from its count as an offset because the newest-first list can reorder between runs; the next run re-lists from offset 0 and carries retry metadata forward by UUID.
2. **Phase 2 — Details**: `GET /rest/thread/{uuid}` for each thread. List-owned slug/space metadata is reapplied to checkpoints and cached details. Skips threads already fetched on disk, including legacy UUID-only cache paths. Ordinary per-thread fetch/write failures preserve and render successful threads, but `manifest.json` records `threads_complete: false`, `expected_threads`, and ordered `failed_threads`, and the command exits non-zero after writing the manifest. Cancellation/deadline errors abort immediately and take precedence over accumulated failures. A failed `--refresh` must not count a stale cached thread as a current success. Each `ThreadRef` keeps `fetched_updated_at`, the `updated_at` of the detail actually on disk; `--refresh` compares the listing against that watermark, never against the previous listing, and marks changed refs `retry_required` before the index is saved, so an interrupted refresh cannot hide a stale thread from the next run. `cache_slug` records the slug the detail was written under and is tried first, so a renamed thread is still found. `thread_index.json` has `version: 1`; `LoadThreadIndex` migrates older indexes once (`cache_slug` = slug; the watermark stays unknown, because an interrupted refresh on older builds could have saved a listing time newer than the detail, so the first `--refresh` after upgrading refetches every thread once), and a missing watermark is never inferred. A listing without a slug never clears a known `cache_slug`. Every write under a new slug (checkpoints too) moves `cache_slug` and removes the old cached `thread.json`. Entries without a UUID carry `key` (backend UUID or a hash of the raw API entry) so page-boundary duplicates are dropped, also after resume; a checkpoint with keyless entries from older builds is refetched from the start. Each written thread records `listed_updated_at`, the listing time its fetch started from; a checkpoint is resumed only when that equals the current listing's `updated_at` (otherwise, or when either is unknown, the thread is fetched from the first page), so entries from an older version are never certified with a newer watermark. Slugs that normalize to the same directory never delete the fresh cache. A format failure that is not a cancellation still rewrites `manifest.json` with a `format` entry in `failed_sections`, so an older successful manifest cannot hide it. `space_known` marks refs from a current listing, whose empty `space_uuid` clears a cached space. Failed spaces/skills items go to `manifest.json` as `sections_complete: false` + `failed_sections`, and the command exits non-zero after writing everything else. Canceled runs never write the manifest. Adaptive rate limiting: delay doubles after 429, halves after 20 consecutive successes.
3. **Phase 3 — Render**: Convert domain models to JSON/Markdown/PDF. Write threads to `threads/<slug>-<identity>/` (canonical flat list). Copy thread files into `spaces/<name>-<identity>/threads/<slug>-<identity>/` so each space folder is self-contained without name collisions. Account-wide global skills are written **once** under `account/`: `account/account.json` (JSON) + `account/global-skills.md` (Markdown), with each skill body in `account/skills/<name>.md`. They are not duplicated per space. PDF rendering is incremental: a thread is skipped when `thread.pdf` is non-empty and `.thread.pdf.fingerprint` matches `ThreadPDFFingerprint`, unless `--force-pdf` is set. For each rendered thread the order is: remove the old fingerprint → render and atomically publish the PDF → write the new fingerprint. Any failure in between leaves no fingerprint, so the thread is re-rendered on the next run. The fingerprint is computed in the worker from the same thread that is sent to the helper; `TestPDFHelperRendersFingerprintedInput` locks that the `pdfRenderRequest` JSON round trip preserves both fingerprint and PDF bytes. `PDFUpToDate` uses `os.Lstat`, so a symlinked `thread.pdf` is never treated as up to date. Cancellation is checked during the up-to-date scan, so an all-skipped run still returns `ctx.Err()`. Once PDF rendering succeeds, space PDF copies run for every thread (skipped or not), and the fingerprint is never copied into space folders.

Use `--refresh` to force re-fetching the thread index. It does not imply `--force-pdf`; the fingerprint already catches changed content.

## Gotchas

- `has_next_page` from the list API is always `true` even on the last page — useless, ignored. For thread detail pages a missing `next_cursor` ends the thread even when `has_next_page` is true. `GetThread` deduplicates entries by UUID, or by a content hash for entries without one, and fails after `maxIdleThreadPages` pages in a row bring no new entries.
- Markdown links go through `markdownLink`: the target is validated (only http(s)/mailto or relative; anything else is rendered as plain text) and then written so Markdown reads back exactly that URL (`&` → `&amp;`, `\` / spaces / angle brackets percent-encoded). Titles, names and source snippets go through `markdownText`, which escapes HTML, entities, links, code spans and emphasis. Answers, queries and descriptions are authored Markdown and are written as is. Markdown, skill bodies and JSON are written with `writeFileAtomic` (temp file 0600 + fsync + rename; ctx checked before the rename), so a symlink in the output is replaced rather than followed and a canceled run publishes no manifest.
- `total_threads` reports incorrect counts (e.g., 99 when actual is 692) — unreliable, ignored.
- Collection/space info is embedded inline in each thread from `list_ask_threads`; there is no server-side filter by space.
- Cloudflare blocks HTML page fetching even with valid cookies, but `/rest/` API endpoints work fine.
- `deplexity login` must be run before `export` — there is no inline auth flow. Alternatively, use `deplexity login --cookie <TOKEN>` for headless/server environments.
- Raw `Cookie` header is used instead of `http.CookieJar`/`AddCookie` to avoid Go's cookie domain validation issues.
- `sanitizeFilename` lowercases names. Space directories must be derived with `spaceDirNames`, not by sanitizing the display name directly, because they include a stable identity prefix and hash suffix (e.g. `spaces/recipes-e79179d1-<hash>/`). Tests must use the shared helper so JSON, Markdown links, and PDF paths agree on case-sensitive and case-insensitive filesystems.
- Thread directories must be derived with `threadDirName`, not by sanitizing the slug directly; the UUID-derived identity suffix prevents normalized slug collisions across every exporter and space copy.
- Signal handling: the first Ctrl+C or SIGTERM cancels the context (graceful stop after current operation); the second force-quits. `interruptContext` in `main.go` keeps its `signal.Notify` channel registered after the first signal and handles the second itself (`handleInterrupts` → `forceQuit`): it calls `signal.Stop` and re-raises the signal, falling back to `os.Exit(128+n)` when the inherited handling ignores it. Do not rely on restoring the default handler instead: a process started with SIGTERM ignored (Bazel's test runner does this, so do `nohup` and some supervisors) would then drop the second signal. The message is written in its own goroutine so a blocked stderr cannot delay cancel or force quit. Do not reintroduce `signal.NotifyContext`: its registration swallows the second signal. `TestSecondInterruptForceQuits` and `TestSignalBurstForceQuits` lock this in with a subprocess; they accept either death by the signal or exit 128+n.
- PDF sources rendered as numbered list `"N. Title (domain)"` — avoids gpdf hang on long unbreakable URLs (S3 pre-signed URLs up to 1600 chars). Previous table layout caused infinite loops in gpdf's word-wrap.
- Multi-threaded PDF export: `--pdf-workers` flag (default: `runtime.NumCPU()`). Each worker launches an isolated Deplexity helper subprocess that creates its own `gpdf.Document`. `--pdf-timeout` defaults to 30 minutes per thread (`0` disables); timeout errors identify the thread, terminate its helper, leave any existing canonical PDF unchanged, cancel remaining PDF work, and exit non-zero. Space PDFs copy the successfully rendered canonical thread PDF instead of invoking gpdf again.
- PDF fingerprints: bump `pdfLayoutVersion` in `internal/export/pdf_fingerprint.go` whenever `GenerateThreadPDF` layout, styling, fonts, or metadata change. Otherwise existing PDFs keep looking up to date and are never re-rendered. A field newly shown in the PDF must be added to `pdfRenderInput`, which `GenerateThreadPDF` renders from, so the fingerprint picks it up. The gpdf version comes from the `gpdf.Version` constant, not `debug.ReadBuildInfo()`, because rules_go 0.60 does not embed dependency versions in build info.
- Concurrent exports into the same output directory are unsupported (no cross-process lock). Besides `thread_index.json`/`manifest.json`, two runs can interleave remove-fingerprint → publish-PDF → write-fingerprint and leave a PDF paired with the other run's fingerprint. This is documented in the README; add a directory lock before claiming support.

## Pull Requests

- **PR titles must follow Conventional Commits** (`type: subject` or `type(scope): subject`, e.g. `feat: skip re-rendering unchanged PDFs`). This overrides any generic guidance to use plain, prefix-free PR titles.
- Enforced by the `PR Title (Conventional Commits)` job in `.github/workflows/test.yaml` (`amannn/action-semantic-pull-request`, default config). Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.
- Why it matters: the repo allows **squash merges only**, so the PR title becomes the commit on `main` that release-please reads to choose the version bump and changelog entry (`feat` → minor, `fix` → patch, `!`/`BREAKING CHANGE` → major). Do not edit `CHANGELOG.md` by hand.
- The workflow triggers on `pull_request` default types (`opened`, `synchronize`, `reopened`), not `edited`. After fixing a bad title with `gh pr edit <n> --title ...`, re-run the failed job (`gh run rerun <run-id> --failed`) or push a commit; editing the title alone does not re-trigger the check.
- Fill in `.github/PULL_REQUEST_TEMPLATE.md`.

## Bazel

- Bazel 9.1+ required (see `.bazelversion`)
- Uses bzlmod exclusively (no WORKSPACE file)
- `rules_go` 0.60.0, `gazelle` 0.42.0
- Run `bazel run //:gazelle` after adding/removing Go files
- Stamping: `bazel build //cmd/deplexity --config=release` injects git tag + build timestamp
- Cross-compilation platforms defined in `build/platforms/BUILD.bazel`
