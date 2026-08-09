package convert

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestOutPaths(t *testing.T) {
	o := Options{Suffix: ".hdr10"}
	in := filepath.Join("in", "movie.mkv")
	out, tmp := o.finalPath(in), tmpPath(in)
	if out != filepath.Join("in", "movie.hdr10.mkv") {
		t.Errorf("finalPath = %q", out)
	}
	if tmp != filepath.Join("in", "movie.dvstrip.tmp.mkv") {
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
	if !IsTemp("/x/movie.dvstrip.tmp.mkv") {
		t.Error("expected temp path to be detected")
	}
	if IsTemp("/x/movie.mkv") {
		t.Error("normal path flagged as temp")
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
