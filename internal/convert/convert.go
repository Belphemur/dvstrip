// Package convert knows how to run the external tools to normalize a Dolby
// Vision file into plain HDR10. All functions are pure wrappers around
// ffmpeg/dovi_tool and use a tmp-then-verify-then-renamed publish step so the
// original file is never at risk of being half-written over.
package convert

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Belphemur/dvstrip/internal/probe"
)

// TempMarker prefixes the source basename to form the in-flight remux
// filename (e.g. .swp.dvstrip.movie.mkv next to movie.mkv). The leading dot
// hides the file from media scanners (Plex/Jellyfin/…), while the real media
// extension stays at the end of the name: ffmpeg guesses the output muxer
// from the filename extension and refuses unknown suffixes ("Unable to
// choose an output format"), so the extension must remain last.
const TempMarker = ".swp.dvstrip."

// legacyTempSuffix is the retired scheme (<name><ext>.dvstrip.tmp). Still
// recognized so scan/watch sweeps remove stale tmps a hard crash may have
// left behind under an older version.
const legacyTempSuffix = ".dvstrip.tmp"

// ffmpeg argument flags used repeatedly across the conversion commands.
const (
	flagMap      = "-map"
	flagMetadata = "-metadata"
	flagNoStats  = "-nostats"
)

// Progress is the sink convert reports ffmpeg progress into. It is
// implemented by display.Tracker; nil means "no progress UI".
type Progress interface {
	Start(key, label string, total int64)
	StartSpinner(key, label string)
	Set(key string, current int64)
	Finish(key string)
}

// Options controls output naming and progress reporting.
type Options struct {
	Suffix   string
	Replace  bool     // overwrite the original after a verified conversion
	Progress Progress // nil disables per-file progress display
}

// finalPath is where the converted file ends up.
func (o Options) finalPath(in string) string {
	if o.Replace {
		return in
	}
	ext := filepath.Ext(in)
	return strings.TrimSuffix(in, ext) + o.Suffix + ext
}

// tmpPath always lives next to the input so the final rename is atomic
// (same directory ⇒ same filesystem ⇒ POSIX rename-over-replace). The marker
// prefixes the basename (dotfile ⇒ hidden from media scanners) rather than
// being appended, so the filename keeps ending in the real media extension
// and ffmpeg's extension-based muxer detection keeps working.
func tmpPath(in string) string {
	return filepath.Join(filepath.Dir(in), TempMarker+filepath.Base(in))
}

// IsTemp reports whether path is an in-flight remux produced by this tool.
func IsTemp(path string) bool {
	return strings.HasPrefix(filepath.Base(path), TempMarker) || strings.HasSuffix(path, legacyTempSuffix)
}

// markerArgs stamps container-level tags so future runs recognize the file.
func markerArgs(from, to string) []string {
	return []string{
		flagMetadata, probe.MarkerKey + "=1",
		flagMetadata, fmt.Sprintf("comment=dvstrip: %s -> %s @ %s",
			from, to, time.Now().UTC().Format(time.RFC3339)),
	}
}

// run executes a command with output wired to the console (no progress UI).
func run(ctx context.Context, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// parseProgressBytes extracts total_size=N lines from ffmpeg -progress output.
// Pure and unit-tested.
func parseProgressBytes(line string) (int64, bool) {
	k, v, ok := strings.Cut(line, "=")
	if !ok || k != "total_size" {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// barLabel builds the per-file bar description "<basename> · <phase>",
// truncating long release names so they don't eat the whole bar width.
func barLabel(src probe.Info, phase string) string {
	name := filepath.Base(src.Path)
	const maxNameLen = 100
	if len(name) > maxNameLen {
		name = name[:maxNameLen-3] + "..."
	}
	return name + " · " + phase
}

// runFFmpeg runs ffmpeg with -nostats and, when a Progress sink is set,
// streams machine-readable -progress output into a per-file bar keyed by the
// source path. ffmpeg stderr is captured and attached to any returned error
// instead of being dumped raw onto the terminal.
func runFFmpeg(ctx context.Context, src probe.Info, o Options, label string, args ...string) error {
	if o.Progress == nil {
		return run(ctx, "ffmpeg", args...)
	}

	full := append([]string{flagNoStats, "-progress", "pipe:1"}, args...)
	cmd := exec.CommandContext(ctx, "ffmpeg", full...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("progress pipe: %w", err)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	var total int64
	if st, statErr := os.Stat(src.Path); statErr == nil {
		total = st.Size()
	}
	o.Progress.Start(src.Path, barLabel(src, label), total)
	defer o.Progress.Finish(src.Path)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if n, ok := parseProgressBytes(scanner.Text()); ok {
			o.Progress.Set(src.Path, n)
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", label, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// runSpinner runs a progress-less command (dovi_tool) under a spinner bar
// when a Progress sink is set.
func runSpinner(ctx context.Context, src probe.Info, o Options, label, name string, args ...string) error {
	if o.Progress == nil {
		return run(ctx, name, args...)
	}
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, &errBuf

	o.Progress.StartSpinner(src.Path, barLabel(src, label))
	defer o.Progress.Finish(src.Path)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// verifyOutput checks the remuxed tmp against the source before publish:
// dimensions preserved, DV actually gone, HDR10+ (when the source carried
// it) still present — stream copy keeps the ST 2094-40 SEI, so its loss
// means the toolchain misbehaved and the output must not be published — and
// the dvstrip marker stamped. Pure and unit-tested.
func verifyOutput(src, out probe.Info) error {
	switch {
	case out.Width != src.Width:
		return fmt.Errorf("verify: width changed %d -> %d", src.Width, out.Width)
	case out.HasDV:
		return fmt.Errorf("verify: Dolby Vision metadata still present after strip")
	case src.HasHDR10Plus && !out.HasHDR10Plus:
		return fmt.Errorf("verify: HDR10+ metadata lost during strip")
	case !out.Processed:
		return fmt.Errorf("verify: dvstrip marker missing in output")
	}
	return nil
}

// publish verifies the remuxed tmp file and makes it visible under its final
// name. On any failure the caller's deferred cleanup removes the tmp file
// and the original is left untouched.
func publish(ctx context.Context, src probe.Info, o Options) (string, error) {
	tmp, final := tmpPath(src.Path), o.finalPath(src.Path)

	out, err := probe.Probe(ctx, tmp)
	if err != nil {
		return "", fmt.Errorf("verify: probe failed: %w", err)
	}
	if err := verifyOutput(src, out); err != nil {
		return "", err
	}

	if err := os.Rename(tmp, final); err != nil { // atomic on POSIX
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// StripDV removes DV metadata (RPU/EL) from an HDR10-compatible DV stream
// (compat 1/6: profiles 7.6, 8.1). Pure stream copy — no re-encode.
// Requires ffmpeg >= 7.1 (hevc_metadata=remove_dovi) or jellyfin-ffmpeg.
//
// The tmp file is cleaned up on every return path (deferred removal), so a
// failed remux, a failed verification, a panic, or os.Exit all leave nothing
// behind. After a successful publish the tmp has already been renamed away,
// making the deferred removal a harmless no-op.
func StripDV(ctx context.Context, src probe.Info, o Options) (string, error) {
	tmp := tmpPath(src.Path)
	defer func() { _ = os.Remove(tmp) }()

	args := []string{
		"-hide_banner", "-loglevel", "error", flagNoStats, "-y",
		"-i", src.Path,
		flagMap, "0", "-c", "copy",
		"-bsf:v", "hevc_metadata=remove_dovi=1",
		"-max_muxing_queue_size", "2048",
	}
	args = append(args, markerArgs("dv", "hdr10")...)
	args = append(args, tmp)
	if err := runFFmpeg(ctx, src, o, "strip DV", args...); err != nil {
		return "", err
	}
	return publish(ctx, src, o)
}

// P5 handles DV profile 5: extract raw HEVC, reshape the RPU from
// profile 5 to 8.1 and discard the enhancement layer with dovi_tool, then
// remux and strip DV metadata to land on HDR10. No pixel re-encoding happens.
//
// CAVEAT: a P5 base layer is not true HDR10 (IPTPQc2). The result renders
// correctly on DV-aware players via the reshaped RPU path, but as plain
// HDR10 the colors are approximate — only a full re-encode fixes that.
func P5(ctx context.Context, src probe.Info, o Options) (string, error) {
	if _, err := exec.LookPath("dovi_tool"); err != nil {
		return "", fmt.Errorf("dovi_tool not found in PATH (set p5-mode=skip to ignore P5)")
	}

	dir, err := os.MkdirTemp("", "dvstrip-p5-")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bl := filepath.Join(dir, "bl.hevc")
	p8 := filepath.Join(dir, "p8.hevc")

	// 1) extract raw annex-b HEVC video.
	if err := runFFmpeg(ctx, src, o, "extract HEVC",
		"-hide_banner", "-loglevel", "error", flagNoStats, "-y",
		"-i", src.Path, flagMap, "0:v:0", "-c:v", "copy",
		"-bsf:v", "hevc_mp4toannexb", "-f", "hevc", bl); err != nil {
		return "", fmt.Errorf("extract hevc: %w", err)
	}

	// 2) reshape RPU: profile 5 -> 8.1, drop the enhancement layer.
	if err := runSpinner(ctx, src, o, "dovi_tool P5→P8.1",
		"dovi_tool", "-m", "2", "convert", "--discard", bl, "-o", p8); err != nil {
		return "", fmt.Errorf("dovi_tool convert: %w", err)
	}

	// 3) remux: video from the converted stream, audio/subs/chapters from the
	//    original, DV metadata stripped, marker stamped.
	tmp := tmpPath(src.Path)
	defer func() { _ = os.Remove(tmp) }()
	args := []string{
		"-hide_banner", "-loglevel", "error", flagNoStats, "-y",
		"-i", p8, "-i", src.Path,
		flagMap, "0:v", flagMap, "1", flagMap, "-1:v",
		"-map_chapters", "1",
		"-c", "copy",
		"-bsf:v", "hevc_metadata=remove_dovi=1",
		"-max_muxing_queue_size", "2048",
	}
	args = append(args, markerArgs("dv-p5", "hdr10")...)
	args = append(args, tmp)
	if err := runFFmpeg(ctx, src, o, "remux + strip", args...); err != nil {
		return "", err
	}
	return publish(ctx, src, o)
}
