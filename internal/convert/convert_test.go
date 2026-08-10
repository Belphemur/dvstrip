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
	if tmp != filepath.Join("in", ".movie.mkv.swp") {
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
	if !IsTemp("/x/.movie.mkv.swp") {
		t.Error("expected temp path to be detected")
	}
	if IsTemp("/x/movie.mkv") {
		t.Error("normal path flagged as temp")
	}
	if IsTemp("/x/movie.hdr10.mkv") {
		t.Error("final output path flagged as temp")
	}
	if IsTemp("/x/movie.swp") {
		t.Error("non-hidden .swp file flagged as temp")
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

func TestOutputFormat(t *testing.T) {
	cases := []struct {
		ext  string
		want string
	}{
		{".mkv", "matroska"},
		{".MKV", "matroska"},
		{".mp4", "mp4"},
		{".m4v", "mp4"},
		{".mov", "mov"},
		{".ts", "mpegts"},
		{".m2ts", "mpegts"},
		{".flac", ""},
	}
	for _, c := range cases {
		if got := outputFormat(c.ext); got != c.want {
			t.Errorf("outputFormat(%q) = %q, want %q", c.ext, got, c.want)
		}
	}
}
