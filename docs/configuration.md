# Configuration reference

Configuration is resolved by [viper](https://github.com/spf13/viper) with this precedence (highest wins):

**CLI flag > environment variable > config file > built-in default**

## Config file discovery

1. `--config /path/to/file.yaml` (explicit)
2. `./dvstrip.yaml` (current directory)
3. `$HOME/.config/dvstrip/dvstrip.yaml`

The file is optional. A fully documented example ships in the repo as [`dvstrip.yaml`](../dvstrip.yaml).

## Environment variables

Prefix `DVSTRIP_`, dashes become underscores:

```bash
DVSTRIP_WORKERS=4 DVSTRIP_REPLACE=true dvstrip scan /media
```

## Keys

| Key | Flag | Env | Default | Description |
|---|---|---|---|---|
| `workers` | `-w, --workers` | `DVSTRIP_WORKERS` | `0` | Concurrent ffmpeg workers. `0` = auto (`NumCPU/2` clamped to `[2,8]`). |
| `extensions` | `-e, --extensions` | `DVSTRIP_EXTENSIONS` | `.mkv .mp4 .ts .m2ts` | Video file extensions considered. Case-insensitive. |
| `dry-run` | `--dry-run` | `DVSTRIP_DRY_RUN` | `false` | Classify everything, write nothing. Wins over `--replace`. |
| `force` | `--force` | `DVSTRIP_FORCE` | `false` | Reprocess files that already carry the `dvstrip` marker tag. |
| `replace` | `--replace` | `DVSTRIP_REPLACE` | `false` | Overwrite originals after verified conversion instead of writing `<suffix>` copies. **No backup is kept.** |
| `suffix` | `--suffix` | `DVSTRIP_SUFFIX` | `.hdr10` | Suffix inserted before the extension of output files. Ignored with `replace`. |
| `p5-mode` | `--p5-mode` | `DVSTRIP_P5_MODE` | `convert` | DV profile 5 handling: `convert` (dovi_tool reshape + strip) or `skip`. |
| `hdr10plus` | `--hdr10plus` | `DVSTRIP_HDR10PLUS` | `false` | Preserve HDR10+ when present (it survives stream copy regardless); log explicit fallback to HDR10 when absent. |
| `debounce` | `--debounce` | `DVSTRIP_DEBOUNCE` | `5s` | Watch mode: how long a file must be quiet before it's processed. |
| `full-scan` | `--full-scan` | `DVSTRIP_FULL_SCAN` | `false` | Watch mode only: enqueue the whole tree once at startup. |
| `log-level` | `--log-level` | `DVSTRIP_LOG_LEVEL` | `info` | `trace` \| `debug` \| `info` \| `warn` \| `error`. |
| `log-json` | `--log-json` | `DVSTRIP_LOG_JSON` | `false` | Emit JSON log lines (handy for Loki/ELK/systemd-journal pipelines). |

## Example `dvstrip.yaml`

```yaml
workers: 0
dry-run: false
force: false
replace: false
suffix: ".hdr10"
p5-mode: convert
hdr10plus: true
full-scan: true
debounce: 5s
extensions: [.mkv, .mp4, .ts, .m2ts]
log-level: info
log-json: false
```

## Recommended starting points

| Scenario | Settings |
|---|---|
| First run on a big library | `dry-run: true`, then `dry-run: false` with defaults |
| Maximum safety, reviewable outputs | `replace: false` (default), delete originals after spot-checking |
| "Just normalize everything, I trust it" | `replace: true` |
| Media server drop folder | `watch` + `full-scan: true`, `debounce: 30s` for slow copies |
| Apple TV / web-dl heavy library | `p5-mode: skip`, handle P5 separately |
