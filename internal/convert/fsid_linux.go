//go:build !darwin

package convert

import (
	"fmt"
	"syscall"
)

// fsKey returns the identity of the filesystem holding path — the statfs
// Fsid pair — so directories sharing a mount share one ledger entry.
func fsKey(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x", uint64(st.Fsid.X__val[0]), uint64(st.Fsid.X__val[1])), nil
}
