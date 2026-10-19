package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Belphemur/dvstrip/internal/convert"
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

// TestWalkVideosReadOnlyLeavesTmpFiles pins the rescan-walk contract: the
// read-only walk must SKIP tmp files without removing them — a periodic
// rescan runs while workers may be mid-publish on a tmp file, and sweeping
// it would break that conversion. The sweeping walk keeps its cleanup
// behavior (crashed-run recovery).
func TestWalkVideosReadOnlyLeavesTmpFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, convert.TempMarker+"movie.mkv")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := walkVideosReadOnly(dir, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("walkVideosReadOnly must not remove tmp files: %v", err)
	}

	if err := walkVideos(dir, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("walkVideos must still sweep tmp files, stat err: %v", err)
	}
}
