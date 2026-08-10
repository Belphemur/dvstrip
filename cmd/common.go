package cmd

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/Belphemur/dvstrip/internal/convert"
	"github.com/Belphemur/dvstrip/internal/probe"
	"github.com/Belphemur/dvstrip/internal/queue"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

// log returns a logger carrying the file path for the current event, so each
// log line is naturally attributable to the file it touches.
func log(path string) zerolog.Logger {
	return pkg.With().Str("file", filepath.Base(path)).Logger()
}

var stats struct {
	scanned, skipped, marked, hdr10Only, stripped, failed atomic.Int64
}

// resolveWorkers returns the configured worker count, or an auto-derived
// default when the user passed 0 / unset.
func resolveWorkers() int {
	if w := viper.GetInt("workers"); w > 0 {
		return w
	}
	return queue.AutoWorkers()
}

// isVideo reports whether path's extension is in the configured allow-list.
func isVideo(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range viper.GetStringSlice("extensions") {
		if ext == strings.ToLower(e) {
			return true
		}
	}
	return false
}

// isOwnOutput reports whether path was produced by a previous/queued dvstrip
// run and must be ignored (marker output or in-flight tmp file).
func isOwnOutput(path string) bool {
	if convert.IsTemp(path) {
		return true
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.Contains(stem, viper.GetString("suffix"))
}

// sweepTemp removes an in-flight remux file left behind by a crashed run
// (SIGKILL / power loss). A well-behaved run deletes its own tmps — on
// success they are renamed away, on failure a deferred os.Remove cleans up —
// so anything still on disk at scan time is necessarily stale. It is a no-op
// for any path that does not carry the temp marker.
func sweepTemp(path string) {
	if !convert.IsTemp(path) {
		return
	}
	l := log(path)
	if err := os.Remove(path); err == nil {
		l.Warn().Msg("removed stale temp file from a crashed run")
	}
}

// submitDir enqueues every video under dir (used by scan and watch --full-scan).
func submitDir(q *queue.Queue, dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if convert.IsTemp(path) {
			sweepTemp(path)
			return nil
		}
		if !isVideo(path) || isOwnOutput(path) {
			return nil
		}
		q.Submit(queue.Job{Path: path})
		return nil
	})
}

// convertOptions builds the shared Options struct from viper config. The
// progress tracker is only attached when it exists — a nil *Tracker stored in
// the Progress interface would not compare == nil.
func convertOptions() convert.Options {
	o := convert.Options{
		Suffix:  viper.GetString("suffix"),
		Replace: viper.GetBool("replace"),
	}
	if tracker != nil {
		o.Progress = tracker
	}
	return o
}

// handle is the queue worker: probe, classify, strip/keep accordingly.
func handle(ctx context.Context, job queue.Job) {
	l := log(job.Path)
	stats.scanned.Add(1)

	info, err := probe.Probe(ctx, job.Path)
	if err != nil {
		l.Error().Err(err).Msg("probe failed")
		stats.failed.Add(1)
		return
	}

	// dvstrip marker already present → skip unless --force.
	if info.Processed && !viper.GetBool("force") {
		l.Info().Str("note", info.ProcessedNote).Msg("already processed, skipping (use --force to redo)")
		stats.marked.Add(1)
		return
	}

	switch {
	case !info.Is4K():
		l.Info().Int("width", info.Width).Int("height", info.Height).Msg("skip: not 4K")
		stats.skipped.Add(1)
	case !info.IsHDR10() && !info.IsDVP5():
		// DV P5 base layers commonly report non-standard color metadata; they
		// are handled below instead of being skipped here.
		l.Info().
			Str("transfer", info.ColorTransfer).
			Str("primaries", info.ColorPrimaries).
			Msg("skip: not HDR10")
		stats.skipped.Add(1)
	case !info.HasDV:
		l.Info().Bool("hdr10+", info.HasHDR10Plus).Msg("already plain HDR10")
		stats.hdr10Only.Add(1)
	default:
		strip(ctx, info, l)
	}
}

func strip(ctx context.Context, info probe.Info, l zerolog.Logger) {
	opts := convertOptions()

	if viper.GetBool("dry-run") {
		l.Info().Str("action", info.Action()).Msg("dry-run: would process")
		return
	}

	var out string
	var err error
	switch {
	case info.IsDVP5():
		if viper.GetString("p5-mode") == "skip" {
			l.Info().Msg("skip: DV profile 5 (p5-mode=skip)")
			stats.skipped.Add(1)
			return
		}
		l.Info().Int("profile", info.DVProfile).Msg("converting P5 -> P8.1 -> HDR10 (approximate colors, no re-encode)")
		out, err = convert.P5(ctx, info, opts)
	case info.DVHDR10Compatible():
		l.Info().Int("profile", info.DVProfile).Int("compat", info.DVCompat).Msg("4K HDR10 + DV → stripping")
		out, err = convert.StripDV(ctx, info, opts)
	default:
		l.Warn().Int("profile", info.DVProfile).Int("compat", info.DVCompat).
			Msg("manual handling required: unsupported DV profile/compat")
		stats.skipped.Add(1)
		return
	}
	if err != nil {
		l.Error().Err(err).Msg("conversion failed")
		stats.failed.Add(1)
		return
	}

	if opts.Replace {
		l.Info().Msg("done: original replaced (DV stripped)")
	} else {
		l.Info().Str("out", filepath.Base(out)).Msg("done")
	}
	stats.stripped.Add(1)

	if viper.GetBool("hdr10plus") {
		switch {
		case info.HasHDR10Plus:
			l.Info().Msg("hdr10+: dynamic metadata preserved through stream copy")
		default:
			l.Info().Msg("hdr10+: no source HDR10+ and DV->HDR10+ synthesis not supported by quietvoid tools; staying HDR10")
		}
	}
}

func printStats() {
	pkg.Info().
		Int64("scanned", stats.scanned.Load()).
		Int64("skipped", stats.skipped.Load()).
		Int64("marked", stats.marked.Load()).
		Int64("already_hdr10", stats.hdr10Only.Load()).
		Int64("stripped", stats.stripped.Load()).
		Int64("failed", stats.failed.Load()).
		Msg("summary")
}
