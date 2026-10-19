package cmd

import (
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

// rescanDecide is the pure change-detection seam of the rescan fallback:
// given the fresh walk results and the previous cache, it returns the paths
// to schedule and the next cache state.
//
// A file is scheduled when it is absent from the previous cache (new) or its
// (size, mtime) differ (changed). Vanished files are naturally absent from
// the next cache, pruning them.
//
// The previous cache starts empty (no --full-scan: the first pass backfills
// everything found, since inotify only reports changes and pre-existing files
// would otherwise never be processed) or is seeded by the full-scan walk
// itself (with --full-scan: the cache then describes exactly what the full
// scan submitted, so the first rescan pass only schedules files that landed
// after that walk — there is no first-pass suppression and no window in which
// a new file could be cached without being scheduled).
func rescanDecide(current, prev rescanCache) (toSchedule []string, next rescanCache) {
	next = make(rescanCache, len(current))
	for path, info := range current {
		next[path] = info
		if prevInfo, seen := prev[path]; seen && prevInfo == info {
			continue
		}
		toSchedule = append(toSchedule, path)
	}
	return toSchedule, next
}
