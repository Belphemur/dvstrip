package convert

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Belphemur/dvstrip/internal/probe"
)

func TestOutPaths(t *testing.T) {
	o := Options{Suffix: ".hdr10"}
	in := filepath.Join("in", "movie.mkv")
	out, tmp := o.finalPath(in), tmpPath(in)
	if out != filepath.Join("in", "movie.hdr10.mkv") {
		t.Errorf("finalPath = %q", out)
	}
	if tmp != filepath.Join("in", ".swp.dvstrip.movie.mkv") {
		t.Errorf("tmpPath = %q", tmp)
	}
}

func TestOutPathsReplace(t *testing.T) {
	o := Options{Replace: true}
	in := "/media/in/movie.mov"
	if o.finalPath(in) != in {
		t.Errorf("finalPath(replace) = %q, want %q", o.finalPath(in), in)
	}
}

func TestIsTemp(t *testing.T) {
	if !IsTemp("/x/.swp.dvstrip.movie.mkv") {
		t.Error("expected temp path to be detected")
	}
	if !IsTemp("/x/movie.mkv.dvstrip.tmp") {
		t.Error("legacy temp suffix must still be detected (crash leftovers)")
	}
	if IsTemp("/x/movie.mkv") {
		t.Error("normal path flagged as temp")
	}
	if IsTemp("/x/movie.hdr10.mkv") {
		t.Error("final output path flagged as temp")
	}
}

func TestStripArgs(t *testing.T) {
	tmp := "/in/.swp.dvstrip.movie.mkv"

	t.Run("hevc", func(t *testing.T) {
		args := stripArgs("/in/movie.mkv", tmp)
		joined := strings.Join(args, " ")

		// The filter must be pinned to the probed video stream: a bare
		// -bsf:v would also hit attached mjpeg covers, which reject it.
		if !slices.Contains(args, "-bsf:v:0") {
			t.Errorf("missing -bsf:v:0 pinning: %s", joined)
		}
		// ffmpeg guesses the output muxer from the last argument's extension.
		if args[len(args)-1] != tmp {
			t.Errorf("output path must be the last argument, got %q", args[len(args)-1])
		}
		for _, want := range []string{"-nostats", "-map 0", "-c copy", doviRpuStrip, probe.MarkerKey + "=1"} {
			if !strings.Contains(joined, want) {
				t.Errorf("args missing %q: %s", want, joined)
			}
		}
	})

	t.Run("av1", func(t *testing.T) {
		args := stripArgs("/in/movie.mkv", tmp)
		joined := strings.Join(args, " ")

		if !slices.Contains(args, "-bsf:v:0") {
			t.Errorf("missing -bsf:v:0 pinning: %s", joined)
		}
		for _, want := range []string{"-nostats", "-map 0", "-c copy", doviRpuStrip, probe.MarkerKey + "=1"} {
			if !strings.Contains(joined, want) {
				t.Errorf("args missing %q: %s", want, joined)
			}
		}
	})
}

func TestP5TimingArgs(t *testing.T) {
	args := p5TimingArgs("/tmp/p8.hevc", "/tmp/timed.mkv", "24000/1001")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-o /tmp/timed.mkv",
		"--default-duration 0:24000/1001p /tmp/p8.hevc", // explicit rational fps
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}

	noDur := p5TimingArgsNoDur("/tmp/p8.hevc", "/tmp/timed.mkv")
	joined = strings.Join(noDur, " ")
	if slices.Contains(noDur, "--default-duration") {
		t.Errorf("fallback must not set explicit duration: %v", noDur)
	}
	if !strings.Contains(joined, "-o /tmp/timed.mkv /tmp/p8.hevc") {
		t.Errorf("fallback missing output+input: %s", joined)
	}
}

func TestP5MergeArgs(t *testing.T) {
	args := p5MergeArgs("/tmp/timed.mkv", "/in/movie.mkv")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-i /tmp/timed.mkv", // timed P5 base layer
		"-i /in/movie.mkv",  // audio/subs/chapters source
		"-map 0:v",          // video from the timed stream
		"-map -1:v",         // drop the original DV video
		"-map_chapters 1",
		doviRpuStrip, // DV metadata stripped at remux
		"-max_muxing_queue_size 2048",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	if args[len(args)-1] != "2048" {
		t.Errorf("merge args must leave the output + marker for the caller: %v", args)
	}
}

func TestVerifyOutput(t *testing.T) {
	base := probe.Info{Width: 3840, Processed: true}
	cases := []struct {
		name    string
		src     probe.Info
		out     probe.Info
		wantErr string // "" expects nil
	}{
		{"ok", base, base, ""},
		{"width changed", base, probe.Info{Width: 1920, Processed: true}, "width changed"},
		{"dv still present", base, probe.Info{Width: 3840, HasDV: true, Processed: true}, "still present"},
		{"hdr10+ preserved", probe.Info{Width: 3840, HasHDR10Plus: true}, probe.Info{Width: 3840, HasHDR10Plus: true, Processed: true}, ""},
		{"hdr10+ lost", probe.Info{Width: 3840, HasHDR10Plus: true}, base, "HDR10+ metadata lost"},
		{"hdr10+ absent in source not required", base, base, ""},
		{"marker missing", base, probe.Info{Width: 3840}, "marker missing"},
	}
	for _, c := range cases {
		err := verifyOutput(c.src, c.out)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error = %v, want substring %q", c.name, err, c.wantErr)
		}
	}
}

func TestParseProgressBytes(t *testing.T) {
	if n, ok := parseProgressBytes("total_size=123456789"); !ok || n != 123456789 {
		t.Errorf("total_size parse: n=%d ok=%v", n, ok)
	}
	if _, ok := parseProgressBytes("out_time_us=123456789"); ok {
		t.Error("non-total_size key must not match")
	}
	if _, ok := parseProgressBytes("total_size=not-a-number"); ok {
		t.Error("invalid number must not match")
	}
}

func TestBarLabel(t *testing.T) {
	short := barLabel(probe.Info{Path: "/x/movie.mkv"})
	if short != "movie.mkv" {
		t.Errorf("short label = %q", short)
	}
	long := barLabel(probe.Info{Path: "/x/" + strings.Repeat("a", 100) + ".mkv"})
	if len(long) > 100 {
		t.Errorf("label not truncated: %d chars", len(long))
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("truncated label missing ellipsis: %q", long)
	}
}

func TestMarkerArgs(t *testing.T) {
	args := markerArgs("dv", "hdr10")
	want := []string{flagMetadata, probe.MarkerKey + "=1", flagMetadata, "comment=dvstrip: dv -> hdr10 @ "}
	// check structure (comment value has a timestamp we can't predict).
	if len(args) != 4 || args[0] != flagMetadata || args[1] != probe.MarkerKey+"=1" || args[2] != flagMetadata {
		t.Fatalf("unexpected marker args: %v", args)
	}
	if !reflect.DeepEqual(args[:3], want[:3]) {
		t.Errorf("marker args head: got %v want %v", args[:3], want[:3])
	}
}
