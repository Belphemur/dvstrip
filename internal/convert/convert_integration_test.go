//go:build integration

package convert

import (
	"context"
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
	out, err := exec.Command("ffmpeg", "-hide_banner", "-h", "bsf=hevc_metadata").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "remove_dovi")
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
	err := runFFmpeg(context.Background(), info, Options{Progress: tr}, "copy test",
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
// Skips when the host ffmpeg predates remove_dovi (< 7.1 / non-jellyfin).
func TestStripDVIntegration(t *testing.T) {
	skipWithoutTools(t)
	if !removeDVISupported(t) {
		t.Skip("host ffmpeg lacks hevc_metadata=remove_dovi (need >= 7.1 or jellyfin-ffmpeg)")
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
