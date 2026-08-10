package convert

import (
	"path/filepath"
	"reflect"
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
	short := barLabel(probe.Info{Path: "/x/movie.mkv"}, "strip DV")
	if short != "movie.mkv · strip DV" {
		t.Errorf("short label = %q", short)
	}
	long := barLabel(probe.Info{Path: "/x/" + strings.Repeat("a", 100) + ".mkv"}, "strip DV")
	if len(long) > 100+len(" · strip DV") {
		t.Errorf("label not truncated: %d chars", len(long))
	}
	if !strings.Contains(long, "... · strip DV") {
		t.Errorf("truncated label missing ellipsis: %q", long)
	}
}

func TestMarkerArgs(t *testing.T) {
	args := markerArgs("dv", "hdr10")
	want := []string{flagMetadata, "dvstrip=1", flagMetadata, "comment=dvstrip: dv -> hdr10 @ "}
	// check structure (comment value has a timestamp we can't predict).
	if len(args) != 4 || args[0] != flagMetadata || args[1] != "dvstrip=1" || args[2] != flagMetadata {
		t.Fatalf("unexpected marker args: %v", args)
	}
	if !reflect.DeepEqual(args[:3], want[:3]) {
		t.Errorf("marker args head: got %v want %v", args[:3], want[:3])
	}
}
