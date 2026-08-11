package convert

import (
	"fmt"
	"syscall"
)

// fsKey returns the identity of the filesystem holding path — the statfs
// Fsid pair — so directories sharing a mount share one ledger entry.
// Darwin spells the Fsid field Val instead of Linux's X__val.
func fsKey(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x", uint64(st.Fsid.Val[0]), uint64(st.Fsid.Val[1])), nil
}
