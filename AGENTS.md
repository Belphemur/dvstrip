# AGENTS.md — dvstrip

Guidance for AI agents (and humans) working in this repository.

## What this project is

`dvstrip` is a Go CLI that finds 4K HDR10 video files carrying Dolby Vision metadata and normalizes them to plain HDR10 **without re-encoding** (stream copy only). It can watch a folder (`fsnotify`), scan one recursively, and mark processed files with a container tag so runs are idempotent.

**Golden rule: never re-encode pixels.** Every transformation is a remux (ffmpeg `-c copy`) plus metadata manipulation. If a change requires decoding/re-encoding video, it is out of scope by design.

## Tech stack & layout

- Go (see `go.mod` for the version), `cobra` + `viper` (CLI/config), `zerolog` (logging), `fsnotify` (watching), `schollz/progressbar/v3` (per-file progress bars). No test frameworks — stdlib `testing` only.
- Module path: `github.com/Belphemur/dvstrip`. All internal imports use the **full module path** (e.g. `github.com/Belphemur/dvstrip/internal/probe`), never a short prefix.
- External runtime tools: `ffmpeg`/`ffprobe` (≥ 7.1 or jellyfin-ffmpeg — must support `hevc_metadata=remove_dovi`), `dovi_tool` (P5 only), `hdr10plus_tool` (optional).

```
cmd/                cobra commands + shared orchestration (no subprocess logic here)
  root.go           flags ↔ viper binding, logger + progress-tracker setup, signal handling
  common.go         handle(): probe → classify → strip; stats; filters
internal/
  probe/            ffprobe wrapper. Probe() = exec + Parse(); Parse() is pure and fixture-tested.
  convert/          ffmpeg/dovi_tool wrappers, progress plumbing, tmp→verify→publish
  display/          multi-bar terminal renderer; owns all terminal writes
  queue/            fixed worker pool, per-path dedup, AutoWorkers
docs/               user/developer documentation (mermaid diagrams)
.github/workflows/  ci.yml (vet/test/lint/goreleaser snapshot) + release.yml (tags → GHCR)
Dockerfile          alpine + jellyfin-ffmpeg + dovi-tool + hdr10plus-tool
.goreleaser.yaml    dockers_v2 only (linux/amd64+arm64); NO archives/checksums (docker-only releases)
```

## Build, test, lint (must all pass before committing)

```bash
go build ./...
go vet ./...
go fix ./...                 # modernize to latest Go idioms; must be a no-op
go test ./... -race -count=1
golangci-lint run        # v2 config in .golangci.yml; CI pins v2.12.2
goreleaser check
goreleaser release --snapshot --clean   # validates docker builds locally
```

Integration tests (real ffmpeg) are build-tagged and skipped by default:

```bash
go test -tags integration ./internal/convert/ -v
```

Note: the StripDV integration test **skips** if the host ffmpeg lacks `remove_dovi` (needs ≥ 7.1 / jellyfin-ffmpeg). The container image always has a working ffmpeg.

## Non-negotiable invariants (do not break these)

1. **Atomicity:** ffmpeg never writes onto the input. Output → `.swp.dvstrip.<name><ext>` next to the source → re-probe verify (parses, same width, no DV, marker present) → `os.Rename`. The tmp is removed by a `defer` on every `StripDV`/`P5` return path (failure, panic, `os.Exit`); stale tmps from a hard crash are swept during scan/watch directory walks. `--replace` is only safe because of this.
2. **Idempotency:** every output carries container tags `dvstrip=1` + `comment=dvstrip: …`; `probe` reads them back via `format_tags` (case-insensitive — MP4 lowercases keys). `--force` is the only bypass.
3. **No feedback loops:** `isOwnOutput()` filters the `.swp.dvstrip.` prefix and the `.hdr10`-style output suffixes from scan/watch inputs; the tmp name also never matches the video extension allow-list.
4. **All terminal output goes through `internal/display.Tracker`** when progress is active (log writer clears bars before printing). Never write directly to stderr/stdout in conversion paths, and never use ffmpeg `-stats` (use `-nostats` + `-progress pipe:1` parsed for `total_size=`). Every bar description must carry the **file basename** via `convert.barLabel()` (truncated to 100 chars) — a bare phase label ("strip DV") is useless with N concurrent workers.
5. **Logging:** zerolog only (`pkg` in cmd), every file-scoped line carries `file=<basename>`. No `fmt.Println`/`log.Printf` in library code.
6. **Decision authority:** classification lives exclusively in `internal/probe` (`Info.Action()` / predicate methods). `cmd` orchestrates, `internal/convert` executes. Keep it that way.

## Coding style

- Guard clauses at the top; flat core logic; parse once at the boundary into typed state (`probe.Info`).
- Pure, testable seams: parsing (`probe.Parse`), progress-line parsing (`convert.parseProgressBytes`), naming (`finalPath`/`tmpPath`). Subprocess execution is thin and untested-by-unit (covered by integration tag).
- Follow golangci-lint v2 config: `errcheck` is enforced (use `_ =` deliberately), `goconst` (extract strings repeated ≥ 4× into constants), `revive` exported comments, `gofumpt` formatting (`golangci-lint fmt`).
- Conventional commits (`feat:`, `fix:`, `ci:`, `docs:`, `build:`, `style:`, `chore:`), logically grouped — one concern per commit.

## Release process

- Releases are tag-driven: `git tag vX.Y.Z && git push origin vX.Y.Z` → `release.yml` runs goreleaser → GitHub Release (changelog) + multi-arch GHCR images (`ghcr.io/belphemur/dvstrip:vX.Y.Z`, `:X.Y`, `:latest`).
- **Docker-only artifacts.** Archives/checksums are deliberately disabled in `.goreleaser.yaml` (`formats: ["none"]`); do not re-enable without a user request.
- Every PR/push to main runs the full snapshot (including both docker platforms under QEMU) — a green PR means a green release.

## Environment gotchas

- Host ffmpeg may be < 7.1 (no `remove_dovi`); the Docker image is the reference environment.
- fsnotify is not recursive — watch mode adds subdirs explicitly; inotify does not work on NFS/SMB mounts.
- Worker pool workers never exit (documented simplification); `Queue.Wait()` is the drain mechanism.
- `Options.Progress` must be left nil when no tracker exists — assigning a typed nil `*display.Tracker` to the interface breaks the `== nil` check (see `convertOptions`).
