// Package convert knows how to run the external tools to normalize a Dolby
// Vision file into plain HDR10. All functions are pure wrappers around
// ffmpeg/dovi_tool and use a tmp-then-verify-then-renamed publish step so the
// original file is never at risk of being half-written over.
package convert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Belphemur/dvstrip/internal/probe"
)

// TempMarker identifies in-flight remux files so scan/watch ignore them.
const TempMarker = ".dvstrip.tmp"

// Options controls output naming.
type Options struct {
	Suffix  string
	Replace bool // overwrite the original after a verified conversion
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
// (same directory ⇒ same filesystem ⇒ POSIX rename-over-replace).
func tmpPath(in string) string {
	ext := filepath.Ext(in)
	return strings.TrimSuffix(in, ext) + TempMarker + ext
}

// IsTemp reports whether path is an in-flight remux produced by this tool.
func IsTemp(path string) bool { return strings.Contains(path, TempMarker) }

// markerArgs stamps container-level tags so future runs recognize the file.
func markerArgs(from, to string) []string {
	return []string{
		"-metadata", probe.MarkerKey + "=1",
		"-metadata", fmt.Sprintf("comment=dvstrip: %s -> %s @ %s",
			from, to, time.Now().UTC().Format(time.RFC3339)),
	}
}

func run(ctx context.Context, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// publish verifies the remuxed tmp file and makes it visible under its final
// name. Verification: re-probe (must parse), preserve source width, DV must
// actually be gone, and the dvstrip marker must be present. On any failure
// the tmp file is removed and the original is left untouched.
func publish(ctx context.Context, src probe.Info, o Options) (string, error) {
	tmp, final := tmpPath(src.Path), o.finalPath(src.Path)

	out, err := probe.Probe(ctx, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("verify: probe failed: %w", err)
	}
	switch {
	case out.Width != src.Width:
		_ = os.Remove(tmp)
		return "", fmt.Errorf("verify: width changed %d -> %d", src.Width, out.Width)
	case out.HasDV:
		_ = os.Remove(tmp)
		return "", fmt.Errorf("verify: Dolby Vision metadata still present after strip")
	case !out.Processed:
		_ = os.Remove(tmp)
		return "", fmt.Errorf("verify: dvstrip marker missing in output")
	}

	if err := os.Rename(tmp, final); err != nil { // atomic on POSIX
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// StripDV removes DV metadata (RPU/EL) from an HDR10-compatible DV stream
// (compat 1/6: profiles 7.6, 8.1). Pure stream copy — no re-encode.
// Requires ffmpeg >= 7.1 (hevc_metadata=remove_dovi) or jellyfin-ffmpeg.
func StripDV(ctx context.Context, src probe.Info, o Options) (string, error) {
	tmp := tmpPath(src.Path)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-stats", "-y",
		"-i", src.Path,
		"-map", "0", "-c", "copy",
		"-bsf:v", "hevc_metadata=remove_dovi=1",
		"-max_muxing_queue_size", "2048",
	}
	args = append(args, markerArgs("dv", "hdr10")...)
	args = append(args, tmp)
	if err := run(ctx, "ffmpeg", args...); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("ffmpeg strip: %w", err)
	}
	return publish(ctx, src, o)
}

// ConvertP5 handles DV profile 5: extract raw HEVC, reshape the RPU from
// profile 5 to 8.1 and discard the enhancement layer with dovi_tool, then
// remux and strip DV metadata to land on HDR10. No pixel re-encoding happens.
//
// CAVEAT: a P5 base layer is not true HDR10 (IPTPQc2). The result renders
// correctly on DV-aware players via the reshaped RPU path, but as plain
// HDR10 the colors are approximate — only a full re-encode fixes that.
func ConvertP5(ctx context.Context, src probe.Info, o Options) (string, error) {
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
	if err := run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", src.Path, "-map", "0:v:0", "-c:v", "copy",
		"-bsf:v", "hevc_mp4toannexb", "-f", "hevc", bl); err != nil {
		return "", fmt.Errorf("extract hevc: %w", err)
	}

	// 2) reshape RPU: profile 5 -> 8.1, drop the enhancement layer.
	if err := run(ctx, "dovi_tool", "-m", "2", "convert", "--discard", bl, "-o", p8); err != nil {
		return "", fmt.Errorf("dovi_tool convert: %w", err)
	}

	// 3) remux: video from the converted stream, audio/subs/chapters from the
	//    original, DV metadata stripped, marker stamped.
	tmp := tmpPath(src.Path)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-stats", "-y",
		"-i", p8, "-i", src.Path,
		"-map", "0:v", "-map", "1", "-map", "-1:v",
		"-map_chapters", "1",
		"-c", "copy",
		"-bsf:v", "hevc_metadata=remove_dovi=1",
		"-max_muxing_queue_size", "2048",
	}
	args = append(args, markerArgs("dv-p5", "hdr10")...)
	args = append(args, tmp)
	if err := run(ctx, "ffmpeg", args...); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("remux: %w", err)
	}
	return publish(ctx, src, o)
}
