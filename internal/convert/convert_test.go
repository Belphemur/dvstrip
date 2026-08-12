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
		bsf, err := bsfForCodec("hevc")
		if err != nil {
			t.Fatalf("bsfForCodec(hevc): %v", err)
		}
		args := stripArgs("/in/movie.mkv", tmp, bsf)
		joined := strings.Join(args, " ")

		// The filter must be pinned to the probed stream: a bare
		// -bsf:v also hits attached mjpeg covers, which reject it (exit 234).
		if !slices.Contains(args, "-bsf:v:0") {
			t.Errorf("missing -bsf:v:0 pinning: %s", joined)
		}
		if slices.Contains(args, "-bsf:v") {
			t.Errorf("bare -bsf:v must not be used (breaks on attached covers): %s", joined)
		}
		// ffmpeg guesses the output muxer from the last argument's extension.
		if args[len(args)-1] != tmp {
			t.Errorf("output path must be the last argument, got %q", args[len(args)-1])
		}
		for _, want := range []string{"-nostats", "-map 0", "-c copy", bsfRemoveDoviHEVC, probe.MarkerKey + "=1"} {
			if !strings.Contains(joined, want) {
				t.Errorf("args missing %q: %s", want, joined)
			}
		}
	})

	t.Run("av1", func(t *testing.T) {
		bsf, err := bsfForCodec("av1")
		if err != nil {
			t.Fatalf("bsfForCodec(av1): %v", err)
		}
		args := stripArgs("/in/movie.mkv", tmp, bsf)
		joined := strings.Join(args, " ")

		if !slices.Contains(args, "-bsf:v:0") {
			t.Errorf("missing -bsf:v:0 pinning: %s", joined)
		}
		for _, want := range []string{"-nostats", "-map 0", "-c copy", bsfRemoveDoviAV1, probe.MarkerKey + "=1"} {
			if !strings.Contains(joined, want) {
				t.Errorf("args missing %q: %s", want, joined)
			}
		}
	})

	t.Run("unsupported codec", func(t *testing.T) {
		_, err := bsfForCodec("vp9")
		if err == nil {
			t.Fatal("expected error for unsupported codec")
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("error should mention unsupported: %v", err)
		}
	})
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
