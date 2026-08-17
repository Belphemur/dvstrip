//go:build integration

package convert

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Belphemur/dvstrip/internal/display"
	"github.com/Belphemur/dvstrip/internal/probe"
)

// Real end-to-end tests against real ffmpeg. Run with:
//
//	go test -tags integration ./internal/convert/ -v

func genClip(t *testing.T, dir string) probe.Info {
	t.Helper()
	src := filepath.Join(dir, "clip.mkv")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=5:size=3840x2160:rate=24",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "keyint=24:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc",
		src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v\n%s", err, out)
	}
	info, err := probe.Probe(context.Background(), src)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !info.Is4K() || !info.IsHDR10() {
		t.Fatalf("generated clip misclassified: %+v", info)
	}
	return info
}

func skipWithoutTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
}

func removeDVISupported(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-h", "bsf=dovi_rpu").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "strip")
}

// TestProgressPipeline exercises the -progress pipe → tracker path with a
// plain stream copy (works on any ffmpeg, no remove_dovi needed).
func TestProgressPipeline(t *testing.T) {
	skipWithoutTools(t)
	dir := t.TempDir()
	info := genClip(t, dir)
	dst := filepath.Join(dir, "copy.mkv")

	tr := display.New(os.Stderr)
	defer tr.Close()
	err := runFFmpeg(context.Background(), info, Options{Progress: tr}, dst, info.Path,
		"-hide_banner", "-loglevel", "error", "-nostats", "-y",
		"-i", info.Path, "-c", "copy", dst)
	if err != nil {
		t.Fatalf("runFFmpeg with progress: %v", err)
	}
	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		t.Fatalf("output missing/empty: %v", err)
	}
}

// TestStripDVIntegration runs the real strip + verify + publish flow.
// Skips when the host ffmpeg predates the dovi_rpu=strip bitstream filter.
func TestStripDVIntegration(t *testing.T) {
	skipWithoutTools(t)
	if !removeDVISupported(t) {
		t.Skip("host ffmpeg lacks dovi_rpu=strip support (need ffmpeg >= 9.0 or a recent jellyfin-ffmpeg)")
	}
	ctx := context.Background()
	dir := t.TempDir()
	info := genClip(t, dir)

	tr := display.New(os.Stderr)
	defer tr.Close()
	out, err := StripDV(ctx, info, Options{Suffix: ".hdr10", Progress: tr})
	if err != nil {
		t.Fatalf("strip: %v", err)
	}

	oi, err := probe.Probe(ctx, out)
	if err != nil {
		t.Fatalf("probe output: %v", err)
	}
	if !oi.Processed {
		t.Error("dvstrip marker missing on output")
	}
	if oi.Width != 3840 || oi.Height != 2160 {
		t.Errorf("resolution changed: %dx%d", oi.Width, oi.Height)
	}
	if oi.HasDV {
		t.Error("output still reports Dolby Vision")
	}
}

// requireBin skips the test when any of the named binaries is missing.
func requireBin(t *testing.T, bins ...string) {
	t.Helper()
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
}

// TestP5Integration regress-tests the P5 (profile 5 → HDR10) conversion, which
// shipped broken in v0.6.3: the raw HEVC base layer carries no timestamps and
// ffmpeg's h265 demuxer never generates them, so the remux aborts with
// "Can't write packet with unknown timestamp". The fix times the stream with
// mkvmerge before the final ffmpeg merge. Skips when dovi_tool or mkvmerge is
// missing, or the host ffmpeg lacks dovi_rpu=strip.
func TestP5Integration(t *testing.T) {
	skipWithoutTools(t)
	requireBin(t, "dovi_tool", "mkvmerge")
	if !removeDVISupported(t) {
		t.Skip("host ffmpeg lacks dovi_rpu=strip support (need ffmpeg >= 9.0 or a recent jellyfin-ffmpeg)")
	}
	ctx := context.Background()

	// The committed fixture is real 4K P5 MKV; copy it to a writable dir so
	// the in-place tmp/output writes don't touch testdata.
	src := filepath.Join(t.TempDir(), "p5.mkv")
	in, err := os.Open("testdata/p5_fixture.mkv")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	out, err := os.Create(src)
	if err != nil {
		t.Fatalf("create copy: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	_ = in.Close()
	_ = out.Close()

	info, err := probe.Probe(ctx, src)
	if err != nil {
		t.Fatalf("probe fixture: %v", err)
	}
	if !info.IsDVP5() {
		t.Fatalf("fixture not classified as P5: %+v", info)
	}

	tr := display.New(os.Stderr)
	defer tr.Close()
	dst, err := P5(ctx, info, Options{Suffix: ".hdr10", Progress: tr})
	if err != nil {
		t.Fatalf("P5 conversion: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output missing: %v", err)
	}

	oi, err := probe.Probe(ctx, dst)
	if err != nil {
		t.Fatalf("probe output: %v", err)
	}
	if oi.Width != info.Width {
		t.Errorf("width changed: %d -> %d", info.Width, oi.Width)
	}
	if oi.HasDV {
		t.Error("output still reports Dolby Vision")
	}
	if !oi.Processed {
		t.Error("dvstrip marker (dvstrip=1) missing on output")
	}
}

func TestStripDVWithCoverArt(t *testing.T) {
	skipWithoutTools(t)
	if !removeDVISupported(t) {
		t.Skip("host ffmpeg lacks dovi_rpu=strip support (need ffmpeg >= 9.0 or a recent jellyfin-ffmpeg)")
	}
	ctx := context.Background()
	dir := t.TempDir()
	base := genClip(t, dir)

	// Attach a cover: an mjpeg "video" stream carrying attached_pic.
	cover := filepath.Join(dir, "cover.jpg")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=1",
		"-frames:v", "1", "-c:v", "mjpeg", cover).CombinedOutput(); err != nil {
		t.Fatalf("generate cover: %v\n%s", err, out)
	}
	withCover := filepath.Join(dir, "withcover.mkv")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-i", base.Path, "-i", cover,
		"-map", "0", "-map", "1", "-c", "copy", "-disposition:v:1", "attached_pic",
		withCover).CombinedOutput(); err != nil {
		t.Fatalf("attach cover: %v\n%s", err, out)
	}
	info, err := probe.Probe(ctx, withCover)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	tr := display.New(os.Stderr)
	defer tr.Close()
	out, err := StripDV(ctx, info, Options{Suffix: ".hdr10", Progress: tr})
	if err != nil {
		t.Fatalf("strip with cover: %v", err)
	}

	chk, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatalf("ffprobe output: %v", err)
	}
	if !strings.Contains(string(chk), "mjpeg") {
		t.Errorf("attached cover lost in output, streams: %s", chk)
	}
}
