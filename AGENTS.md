# AGENTS.md — dvstrip

Guidance for AI agents (and humans) working in this repository.

## What this project is

`dvstrip` is a Go CLI that finds 4K HDR10 video files carrying Dolby Vision metadata and normalizes them to plain HDR10 **without re-encoding** (stream copy only). It can watch a folder (`fsnotify`), scan one recursively, and mark processed files with a container tag so runs are idempotent.

**Golden rule: never re-encode pixels.** Every transformation is a remux (ffmpeg `-c copy`) plus metadata manipulation. If a change requires decoding/re-encoding video, it is out of scope by design.

## Tech stack & layout

- Go (see `go.mod` for the version), `cobra` + `viper` (CLI/config), `zerolog` (logging), `fsnotify` (watching), `vbauerster/mpb/v8` (per-file progress bars). No test frameworks — stdlib `testing` only.
- Module path: `github.com/Belphemur/dvstrip`. All internal imports use the **full module path** (e.g. `github.com/Belphemur/dvstrip/internal/probe`), never a short prefix.
- External runtime tools: `ffmpeg`/`ffprobe` (≥ 9.0 or a recent jellyfin-ffmpeg with `dovi_rpu=strip=1` support), `dovi_tool` (P5 only), `mkvmerge` (P5 timing recovery), `hdr10plus_tool` (optional).

```
cmd/                cobra commands + shared orchestration (no subprocess logic here)
  root.go           flags ↔ viper binding, logger + progress-tracker setup, signal handling
  common.go         handle(): probe → classify → strip; stats; filters; walkVideos (shared dir-walk eligibility)
  watch.go          fsnotify loop, new-directory pickup; scheduler.Close() flush before q.Wait() on shutdown
  watch_scheduler.go fileScheduler: size-settling debounce, generation-guarded timers, Close() flush
internal/
  probe/            ffprobe wrapper. Probe() = exec + Parse(); Parse() is pure and fixture-tested.
  convert/          ffmpeg/dovi_tool/mkvmerge wrappers, progress plumbing, tmp→verify→publish
  display/          multi-bar terminal renderer; owns all terminal writes
  queue/            fixed worker pool, per-path dedup, AutoWorkers
docs/               user/developer documentation (mermaid diagrams)
.github/workflows/  ci.yml (vet/test/lint/integration/goreleaser snapshot → single `gate` job for branch protection) + release.yml (tags → GHCR)
renovate.json       Renovate: automerge every dependency PR once `gate` is green (needs the Mend app installed + repo allow_auto_merge)
Dockerfile          alpine + jellyfin-ffmpeg + dovi-tool + mkvtoolnix + hdr10plus-tool
.goreleaser.yaml    Linux binaries (amd64+arm64 tar.gz + checksums on the GitHub Release) + dockers_v2 (linux/amd64+arm64)
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

Note: the StripDV integration tests **skip** if the host ffmpeg lacks `dovi_rpu=strip=1` support (needs ffmpeg ≥ 9.0 or a recent jellyfin-ffmpeg); the P5 integration test additionally needs `dovi_tool` and `mkvmerge` on PATH and skips otherwise. CI runs them in the `integration` job against a pinned portable jellyfin-ffmpeg (version in `ci.yml`'s `JELLYFIN_FFMPEG`) with mkvtoolnix installed. The container image always has a working toolchain.

## Non-negotiable invariants (do not break these)

1. **Atomicity:** ffmpeg never writes onto the input. Output → `.swp.dvstrip.<name><ext>` (dotfile prefix hides it from media scanners; the real extension stays **last** because ffmpeg picks the output muxer from the filename extension — a `.tmp` suffix aborts with "Unable to choose an output format") next to the source → re-probe verify (parses, same width, no DV, HDR10+ preserved when the source had it, marker present) → `os.Rename`. The tmp is removed by a `defer` on every `StripDV`/`P5` return path (failure, panic, `os.Exit`); stale tmps from a hard crash (including the retired `*.dvstrip.tmp` suffix) are swept during scan/watch directory walks. `--replace` is only safe because of this.
2. **Idempotency:** every output carries container tags `dvstrip=1` + `comment=dvstrip: …`; `probe` reads them back via `format_tags` (case-insensitive — MP4 lowercases keys). `--force` is the only bypass.
3. **No feedback loops:** `isOwnOutput()` filters the `.swp.dvstrip.` basename prefix (`convert.IsTemp`) and the `.hdr10`-style output suffixes from scan/watch inputs. The tmp name *does* match the video extension allow-list (it must, for ffmpeg), so the prefix check is the explicit guard and runs before the extension filter in directory walks.
4. **All terminal output goes through `internal/display.Tracker`** when progress is active (on a TTY log writes print above the bars via mpb). Never write directly to stderr/stdout in conversion paths, and never use ffmpeg `-stats` (use `-nostats` + `-progress pipe:1` parsed for `total_size=`). Every bar description must carry the **file basename** via `convert.barLabel()` (truncated to 100 chars) — a bare phase label ("strip DV") is useless with N concurrent workers. On a non-TTY (docker logs, CI) bars don't render — the tracker emits a `progress: <file> N%` line every 10% instead, so captured logs stay clean (no ANSI codes, no per-redraw line spam).
5. **Logging:** zerolog only (`pkg` in cmd), every file-scoped line carries `file=<basename>`. No `fmt.Println`/`log.Printf` in library code.
6. **Decision authority:** classification lives exclusively in `internal/probe` (`Info.Action()` / predicate methods). `cmd` orchestrates, `internal/convert` executes. Keep it that way.

## Coding style

- Guard clauses at the top; flat core logic; parse once at the boundary into typed state (`probe.Info`).
- Pure, testable seams: parsing (`probe.Parse`), progress-line parsing (`convert.parseProgressBytes`), naming (`finalPath`/`tmpPath`), ffmpeg command building (`convert.stripArgs`), output verification (`convert.verifyOutput`). Subprocess execution is thin and untested-by-unit (covered by integration tag).
- Follow golangci-lint v2 config: `errcheck` is enforced (use `_ =` deliberately), `goconst` (extract strings repeated ≥ 4× into constants), `revive` exported comments, `gofumpt` formatting (`golangci-lint fmt`).
- Conventional commits (`feat:`, `fix:`, `ci:`, `docs:`, `build:`, `style:`, `chore:`), logically grouped — one concern per commit.

## Release process

- Releases are tag-driven: `git tag vX.Y.Z && git push origin vX.Y.Z` → `release.yml` runs goreleaser → GitHub Release (changelog + Linux amd64/arm64 tarballs + checksums) + multi-arch GHCR images (`ghcr.io/belphemur/dvstrip:vX.Y.Z`, `:X.Y`, `:latest`).
- **Artifacts: Linux executables + Docker images.** `.goreleaser.yaml` ships `tar.gz` archives with checksums (added per user request — previously docker-only) alongside the dockers_v2 images; keep both in sync when touching the file.
- Every PR/push to main runs the full snapshot (including both docker platforms under QEMU) — a green PR means a green release.

## Environment gotchas

- Host ffmpeg may be older than the `dovi_rpu` filter (ffmpeg ≥ 9.0 or a recent jellyfin-ffmpeg); the Docker image is the reference environment. On Arch/CachyOS: `sudo pacman -S jellyfin-ffmpeg` then symlink `/usr/lib/jellyfin-ffmpeg/{ffmpeg,ffprobe}` into `/usr/local/bin` (same as the Dockerfile) so the bare `ffmpeg` name resolves to the jellyfin build.
- fsnotify is not recursive — watch mode adds subdirs explicitly; inotify does not work on NFS/SMB mounts.
- Watch-mode events never reach the queue directly: `fileScheduler` holds each path until its size is stable for the debounce window. Callbacks are generation-guarded (a stopped-but-fired `AfterFunc` is ignored), removed files are dropped without submitting, transient `Stat` errors retry, and `scheduler.Close()` flushes pending files before `q.Wait()` on shutdown.
- Worker pool workers never exit (documented simplification); `Queue.Wait()` is the drain mechanism.
- `Options.Progress` must be left nil when no tracker exists — assigning a typed nil `*display.Tracker` to the interface breaks the `== nil` check (see `convertOptions`).
