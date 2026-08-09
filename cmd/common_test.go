package cmd

import (
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
		{"/x/movie.dvstrip.tmp.mkv", true}, // temp file is still "video ext"
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
		{"/x/movie.dvstrip.tmp.mkv", true},
		{"/x/movie.mkv", false},
	}
	for _, c := range cases {
		if got := isOwnOutput(c.path); got != c.want {
			t.Errorf("isOwnOutput(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
