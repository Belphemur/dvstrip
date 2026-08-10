package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestIsVideo(t *testing.T) {
	viper.Set("extensions", []string{".mkv", ".mp4", ".ts", ".m2ts"})
	cases := []struct {
		path string
		want bool
	}{
		{"/x/movie.MKV", true},
		{"/x/clip.mp4", true},
		{"/x/song.flac", false},
		{"/x/.swp.dvstrip.movie.mkv", true}, // in-flight temp keeps the media extension; convert.IsTemp is the guard
	}
	for _, c := range cases {
		if got := isVideo(c.path); got != c.want {
			t.Errorf("isVideo(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsOwnOutput(t *testing.T) {
	viper.Set("suffix", ".hdr10")
	cases := []struct {
		path string
		want bool
	}{
		{"/x/movie.hdr10.mkv", true},
		{"/x/movie.hdr10.tmp.mkv", true},
		{"/x/.swp.dvstrip.movie.mkv", true},
		{"/x/movie.mkv", false},
	}
	for _, c := range cases {
		if got := isOwnOutput(c.path); got != c.want {
			t.Errorf("isOwnOutput(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestSweepTemp(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".swp.dvstrip.movie.mkv")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write stale tmp: %v", err)
	}

	sweepTemp(stale)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale tmp not removed: %v", err)
	}

	kept := filepath.Join(dir, "movie.hdr10.mkv")
	if err := os.WriteFile(kept, []byte("done"), 0o600); err != nil {
		t.Fatalf("write kept file: %v", err)
	}
	sweepTemp(kept)
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("non-temp file must not be removed: %v", err)
	}
}
