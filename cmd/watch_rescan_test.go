package cmd

import (
	"testing"
	"time"
)

const (
	rescanTestPathA = "/a.mkv"
	rescanTestPathB = "/b.mkv"
)

// TestRescanDecide pins the pure change-detection seam of the periodic
// rescan fallback: given walk results and the previous cache, it must
// schedule exactly the new/changed files, prune vanished entries, and honour
// the first-pass/full-scan contract. No ffmpeg involved — this is pure map
// arithmetic on (size, mtime).
func TestRescanDecide(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)

	mk := func(size int64, mtime time.Time) rescanInfo {
		return rescanInfo{size: size, mtime: mtime}
	}

	t.Run("new file is scheduled and cached", func(t *testing.T) {
		current := rescanCache{rescanTestPathA: mk(100, now)}
		scheduled, next := rescanDecide(current, rescanCache{}, false, false)
		if len(scheduled) != 1 || scheduled[0] != rescanTestPathA {
			t.Errorf("expected [%s] scheduled, got %v", rescanTestPathA, scheduled)
		}
		want := rescanInfo{size: 100, mtime: now}
		if got, ok := next[rescanTestPathA]; !ok || got != want {
			t.Errorf("expected %s cached with state %+v, got %+v (present: %v)", rescanTestPathA, want, got, ok)
		}
	})

	t.Run("changed size is scheduled", func(t *testing.T) {
		prev := rescanCache{rescanTestPathA: mk(100, now)}
		current := rescanCache{rescanTestPathA: mk(200, now)}
		scheduled, next := rescanDecide(current, prev, false, false)
		if len(scheduled) != 1 || scheduled[0] != rescanTestPathA {
			t.Errorf("expected [%s] scheduled, got %v", rescanTestPathA, scheduled)
		}
		if next[rescanTestPathA].size != 200 {
			t.Errorf("expected next cache size 200, got %d", next[rescanTestPathA].size)
		}
	})

	t.Run("changed mtime only is scheduled", func(t *testing.T) {
		prev := rescanCache{rescanTestPathA: mk(100, old)}
		current := rescanCache{rescanTestPathA: mk(100, now)}
		scheduled, next := rescanDecide(current, prev, false, false)
		if len(scheduled) != 1 || scheduled[0] != rescanTestPathA {
			t.Errorf("expected [%s] scheduled, got %v", rescanTestPathA, scheduled)
		}
		if next[rescanTestPathA].mtime.Equal(old) {
			t.Error("expected next cache to carry the new mtime")
		}
	})

	t.Run("unchanged file is neither scheduled nor dropped", func(t *testing.T) {
		state := rescanInfo{size: 100, mtime: now}
		prev := rescanCache{rescanTestPathA: state}
		current := rescanCache{rescanTestPathA: state}
		scheduled, next := rescanDecide(current, prev, false, false)
		if len(scheduled) != 0 {
			t.Errorf("expected nothing scheduled, got %v", scheduled)
		}
		if got := next["/a.mkv"]; got != state {
			t.Errorf("expected cache to keep state %+v, got %+v", state, got)
		}
	})

	t.Run("vanished file is pruned from the cache", func(t *testing.T) {
		prev := rescanCache{
			rescanTestPathA: mk(100, now),
			rescanTestPathB: mk(200, now),
		}
		current := rescanCache{rescanTestPathA: mk(100, now)}
		scheduled, next := rescanDecide(current, prev, false, false)
		if len(scheduled) != 0 {
			t.Errorf("expected nothing scheduled, got %v", scheduled)
		}
		if _, ok := next[rescanTestPathB]; ok {
			t.Error("expected vanished " + rescanTestPathB + " pruned from the next cache")
		}
		if _, ok := next[rescanTestPathA]; !ok {
			t.Error("expected surviving " + rescanTestPathA + " kept in the next cache")
		}
	})

	t.Run("first pass with full scan populates cache without scheduling", func(t *testing.T) {
		current := rescanCache{
			rescanTestPathA: mk(100, now),
			rescanTestPathB: mk(200, now),
		}
		scheduled, next := rescanDecide(current, rescanCache{}, true, true)
		if len(scheduled) != 0 {
			t.Errorf("expected the full scan to own the initial herd (nothing scheduled), got %v", scheduled)
		}
		if len(next) != 2 {
			t.Errorf("expected cache populated with 2 entries, got %d", len(next))
		}
	})

	t.Run("first pass without full scan backfills everything", func(t *testing.T) {
		current := rescanCache{
			rescanTestPathA: mk(100, now),
			rescanTestPathB: mk(200, now),
		}
		scheduled, next := rescanDecide(current, rescanCache{}, true, false)
		if len(scheduled) != 2 {
			t.Errorf("expected first pass without full scan to backfill all 2 files, got %v", scheduled)
		}
		if len(next) != 2 {
			t.Errorf("expected cache populated with 2 entries, got %d", len(next))
		}
	})
}
