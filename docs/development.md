# Development

## Prerequisites

- Go ≥ 1.26
- golangci-lint v2
- goreleaser v2
- docker with buildx (for the container part of the pipeline)
- ffmpeg/ffprobe/dovi_tool on PATH only if you want to run the real binary locally (unit tests don't need them). For the integration tests you need `dovi_rpu=strip=1` support (ffmpeg ≥ 9.0 or a recent jellyfin-ffmpeg). On Arch/CachyOS: `sudo pacman -S jellyfin-ffmpeg`, then symlink its binaries like the Dockerfile does: `sudo ln -sf /usr/lib/jellyfin-ffmpeg/{ffmpeg,ffprobe} /usr/local/bin/`

## Layout

```
cmd/                cobra commands + shared handler
  root.go           flags/config/logging/signals
  common.go         probe → classify → strip logic, queue glue
  check.go|scan.go|watch.go
internal/
  probe/            ffprobe wrapper + pure parser (Parse) — fully unit-tested from fixtures
  convert/          ffmpeg/dovi_tool wrappers, tmp→verify→publish protocol
  queue/            worker pool with path dedup
docs/               this documentation
```

## Test & lint

```bash
go vet ./...
go test ./... -race -count=1
golangci-lint run ./...
```

Test philosophy:

- `probe` tests never execute ffprobe — they feed recorded JSON fixtures through `probe.Parse`. The detection matrix (HDR10+DV p7/p8, P5, plain HDR10, SDR, HDR10+, already-marked) is covered there.
- `convert` tests cover naming/marker/temp helpers and the generated ffmpeg command line (`stripArgs`, pure). The ffmpeg paths are exercised end-to-end by build-tagged integration tests (`go test -tags integration ./internal/convert/ -v`), which run in CI against a pinned portable jellyfin-ffmpeg.
- `queue` tests cover dedup, drain, and `AutoWorkers` bounds, under `-race`.

## CI/CD

Two workflows in `.github/workflows/`:

- **ci.yml** (push/PR to main): `go vet`, `go test -race`, golangci-lint, an **integration** job (real StripDV runs incl. the attached-cover regression, against a pinned portable jellyfin-ffmpeg), then a full **goreleaser snapshot** — builds both linux binaries and both docker platforms (via QEMU + buildx) without pushing. If a PR breaks the Dockerfile or the goreleaser config, CI catches it. All jobs funnel into a single **`gate`** job — mark only `gate` as required in branch protection; it fails if any job fails or is skipped.
- **Renovate** (`renovate.json`, hosted Mend app): `automerge` via GitHub auto-merge — every dependency PR merges itself once the `gate` check is green. Requires the Renovate app installed on the repo and `allow_auto_merge` enabled in repo settings.
- **release.yml** (tags `v*`): checkout with full history (for the changelog), QEMU + buildx, GHCR login with `GITHUB_TOKEN`, `goreleaser release --clean`.

Repository needs no extra secrets: the built-in `GITHUB_TOKEN` gets `contents: write` (GitHub Release) and `packages: write` (GHCR).

## Cutting a release

```bash
git tag v0.1.0
git push origin main --tags
```

The release workflow publishes:

- GitHub Release with auto-generated changelog (`changelog: use: github` in `.goreleaser.yaml`)
- Linux executables on the release: `dvstrip_vX.Y.Z_linux_{amd64,arm64}.tar.gz` (binary + README + LICENSE) plus a `checksums.txt`
- `ghcr.io/belphemur/dvstrip:v0.1.0`, `:0.1`, `:latest` — multi-arch manifest (`linux/amd64` + `linux/arm64`)

## Local release dry-run

```bash
goreleaser check                      # validate config
goreleaser release --snapshot --clean # build everything locally, push nothing
```

Snapshot images are tagged `...-amd64` / `...-arm64` locally and never leave your machine.

## Code conventions

- Guard clauses early, flat core logic.
- Parse at the boundary (`probe.Parse`), keep the internals on typed state (`probe.Info`).
- All subprocess execution lives in `internal/convert`; all classification in `internal/probe`. `cmd` only orchestrates.
- Everything is logged through the package zerolog instance with a `file=` field — no `fmt.Println` outside `check`'s structured output.
