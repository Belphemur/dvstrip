package cmd

import (
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestShouldProcessWatchEvent(t *testing.T) {
	cases := []struct {
		name string
		op   fsnotify.Op
		want bool
	}{
		{"Create", fsnotify.Create, true},
		{"Write", fsnotify.Write, true},
		{"Rename", fsnotify.Rename, true},
		{"Chmod", fsnotify.Chmod, false},
		{"Remove", fsnotify.Remove, false},
		{"Create|Write", fsnotify.Create | fsnotify.Write, true},
		{"Write|Chmod", fsnotify.Write | fsnotify.Chmod, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := fsnotify.Event{Op: tc.op}
			if got := shouldProcessWatchEvent(ev); got != tc.want {
				t.Fatalf("shouldProcessWatchEvent(%v) = %v, want %v", tc.op, got, tc.want)
			}
		})
	}
}
