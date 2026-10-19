package cmd

import (
	"syscall"
)

// Magic filesystem type values (Linux statfs). These are the known
// network/federation filesystems on which inotify watches only see writers
// that go through the SAME mount view the watcher holds — a writer using a
// different mount of the same data (Unraid SHFS over a pool mount, NFS, SMB)
// is invisible to fsnotify.
const (
	fsTypeNFS  = 0x6969
	fsTypeCIFS = 0xFF534D42
	fsTypeFUSE = 0x65735546
)

// warnIfInotifyBlind logs a warning when the watched directory sits on a
// filesystem where fsnotify cannot observe writers on other mounts. The
// periodic rescan (rescan-interval) is the safety net for exactly this case.
func warnIfInotifyBlind(dir string) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		pkg.Warn().Err(err).Str("dir", dir).Msg("rescan: could not stat filesystem type; inotify visibility unknown")
		return
	}
	switch st.Type {
	case fsTypeNFS, fsTypeCIFS, fsTypeFUSE:
		pkg.Warn().
			Str("dir", dir).
			Msg("watched dir is on NFS/SMB/FUSE: inotify only sees writers through this exact mount, so changes written via another mount view (e.g. Unraid SHFS) will not fire events — rescan-interval is the safety net")
	}
}
