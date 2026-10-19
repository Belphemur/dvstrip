package cmd

import (
	"os"
	"time"
)

// rescanInfo captures the observed state of one video file at walk time, so a
// periodic rescan can decide whether a file is new or changed without
// re-probing the whole library. Comparing (size, mtime) is deliberately cheap
// — resubmitting unchanged files every tick would re-queue the entire
// library, which is the exact failure mode this fallback exists to avoid.
type rescanInfo struct {
	size  int64
	mtime time.Time
}

// rescanCache maps an absolute path to its last observed state. Entries for
// files that disappeared are pruned on every pass, keeping memory bounded by
// the live tree.
type rescanCache map[string]rescanInfo

// buildRescanSnapshot walks dir with the shared eligibility rules
// (walkVideos: extension allow-list, own-output and in-flight tmp filtering)
// and records (size, mtime) for every processable file. A file that vanishes
// between the walk and its stat is skipped with a warning.
func buildRescanSnapshot(dir string) (rescanCache, error) {
	snap := make(rescanCache)
	err := walkVideos(dir, func(path string) {
		info, statErr := os.Stat(path)
		if statErr != nil {
			l := log(path)
			l.Warn().Err(statErr).Msg("rescan: stat failed, skipping file")
			return
		}
		snap[path] = rescanInfo{size: info.Size(), mtime: info.ModTime()}
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// rescanDecide is the pure change-detection seam of the rescan fallback:
// given the fresh walk results, the previous cache, and whether the initial
// full scan already submitted everything, it returns the paths to schedule
// and the next cache state.
//
// A file is scheduled when it is absent from the previous cache (new) or its
// (size, mtime) differ (changed). Vanished files are naturally absent from
// the next cache, pruning them.
//
// First-pass semantics: the first tick runs with an empty prev cache. When
// --full-scan was enabled, submitDir already submitted the whole tree, so the
// first pass must populate the cache WITHOUT scheduling (no duplicate
// thundering herd). Without a full scan, the first pass is a backfill: the
// scheduler receives every file found, because inotify only reports changes
// and pre-existing files would otherwise never be processed.
func rescanDecide(current, prev rescanCache, isFirstPass, fullScanRan bool) (toSchedule []string, next rescanCache) {
	next = make(rescanCache, len(current))
	for path, info := range current {
		next[path] = info
		prevInfo, seen := prev[path]
		if seen && prevInfo == info {
			continue
		}
		if isFirstPass && fullScanRan {
			// The full scan owns these files; only record their state.
			continue
		}
		toSchedule = append(toSchedule, path)
	}
	return toSchedule, next
}
