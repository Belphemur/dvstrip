# Running dvstrip in Docker

The official image bundles everything — no host dependencies:

| Component | Source | Version |
|---|---|---|
| ffmpeg / ffprobe | Alpine `jellyfin-ffmpeg` package (symlinked onto PATH from `/usr/lib/jellyfin-ffmpeg/`) | recent Jellyfin build with `dovi_rpu=strip=1` support |
| dovi_tool | Alpine edge/community `dovi-tool` | 2.3.x |
| hdr10plus_tool | Alpine edge/community `hdr10plus-tool` | 1.7.x |
| dvstrip | static Go binary (`CGO_ENABLED=0`) | the release tag |

Everything is native musl (Alpine) — no libc mismatches anywhere.

## Tags

Published to `ghcr.io/belphemur/dvstrip` as a multi-arch manifest (`linux/amd64`, `linux/arm64`):

- `:latest` — newest release
- `:v1.2.3` — exact version
- `:1.2` — major.minor

## Basic usage

```bash
# one-shot scan (read-only classification)
docker run --rm -v /media/movies:/media/movies \
  ghcr.io/belphemur/dvstrip:latest scan /media/movies --dry-run

# normalize, side-by-side outputs
docker run --rm -v /media/movies:/media/movies \
  ghcr.io/belphemur/dvstrip:latest scan /media/movies

# in-place
docker run --rm -v /media/movies:/media/movies \
  ghcr.io/belphemur/dvstrip:latest scan /media/movies --replace
```

The entrypoint is `dvstrip`, so everything after the image name is passed as CLI args.

## Daemon / drop-folder mode

```bash
docker run -d \
  --name dvstrip \
  --restart unless-stopped \
  -v /media/incoming:/incoming \
  ghcr.io/belphemur/dvstrip:latest \
  watch /incoming --full-scan --debounce 30s --log-json
```

- `--full-scan` processes the backlog once, then watches for new files.
- `--debounce 30s` avoids touching files still being copied (SMB/NFS copies of 50GB remuxes take minutes).
- `--log-json` makes `docker logs` machine-parseable.
- **Progress bars are on by default.** With a TTY (`docker run -t`) each in-flight conversion gets its own bar, redrawn in place. Without a TTY the bars don't render — instead a `progress: <file> N%` log line is emitted every 10%, so captured logs stay clean (no ANSI control codes, no per-redraw line spam) while still showing progress. Pass `--no-progress` or `--log-json` to silence progress entirely.

## docker-compose

```yaml
services:
  dvstrip:
    image: ghcr.io/belphemur/dvstrip:latest
    restart: unless-stopped
    volumes:
      - /media/incoming:/incoming
      - ./dvstrip.yaml:/etc/dvstrip.yaml:ro
    command: ["--config", "/etc/dvstrip.yaml", "watch", "/incoming", "--full-scan"]
```

## NAS / unRAID notes

- The container runs as **root by default**; files it writes are root-owned. On unRAID/NAS setups you usually want `-e PUID/PGID` behavior — the image doesn't ship s6, so either run with `--user 99:100` (unRAID `nobody:users`) or fix ownership with a cron `chown`. If you use `--user`, make sure that user can write the media directory.
- Scanning a large library is I/O-bound; `--workers` above ~4 on HDD arrays buys nothing.
- Watch mode on network mounts: fsnotify uses inotify, which **does not work on NFS/SMB mounts**. Point the watch at the local export, not the client mount.

## Verifying an image you built locally

```bash
goreleaser release --snapshot --clean
docker run --rm ghcr.io/belphemur/dvstrip:latest-amd64 \
  check /nonexistent.mkv   # expect an ffprobe error, not "binary not found"
```
