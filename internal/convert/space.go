package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// spaceHeadroom is the slack added on top of the source size when reserving
// output space: stream copy is close to 1:1 but container overhead and remux
// jitter can push the output a few percent past the input.
const spaceHeadroom = 1.05

// waitPollInterval is how often free space is re-checked while a replace-mode
// job waits for room. Short enough to start soon after space frees up, long
// enough to keep statfs spam negligible.
const waitPollInterval = 5 * time.Second

// ErrNoSpace is returned when a conversion cannot start because the
// destination filesystem is too small (free space minus what the already
// running jobs still need). Replace mode waits instead of failing and never
// returns this error.
var ErrNoSpace = errors.New("not enough free disk space")

// SpaceGuard tracks how much room the in-flight conversions still need per
// filesystem, so N concurrent workers can never collectively exceed the free
// space. Create one per run and share it with every conversion through
// Options.Space; the zero value is ready to use.
type SpaceGuard struct {
	mu       sync.Mutex
	reserved map[string]int64 // directory -> bytes the running jobs still need
}

// NewSpaceGuard returns a ready-to-use guard.
func NewSpaceGuard() *SpaceGuard { return &SpaceGuard{reserved: map[string]int64{}} }

// freeSpace returns the bytes available to unprivileged processes on the
// filesystem holding path.
func freeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // bavail*bsize fits int64 on every supported platform
}

// reserve registers need extra bytes for dir, succeeding only while the
// projected space left after every running job finishes stays non-negative —
// that projection is the "clear view of the final space left" that keeps
// parallel jobs from overrunning the disk. On failure nothing is reserved
// and false is returned.
func (g *SpaceGuard) reserve(dir string, need int64) bool {
	free, err := freeSpace(dir)
	if err != nil {
		// Unreadable/odd mount: don't block the conversion on a statfs
		// failure; ffmpeg failing with ENOSPC is reported like any other
		// conversion error.
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if free-g.reserved[dir] < need {
		return false
	}
	g.reserved[dir] += need
	return true
}

// shrink reduces the reservation for dir as bytes land on disk, so the
// projected final free space stays accurate for other jobs waiting to start.
func (g *SpaceGuard) shrink(dir string, n int64) {
	if n <= 0 {
		return
	}
	g.mu.Lock()
	g.reserved[dir] -= n
	if g.reserved[dir] < 0 {
		g.reserved[dir] = 0
	}
	g.mu.Unlock()
}

// release drops the reservation entirely (conversion done or aborted).
func (g *SpaceGuard) release(dir string) {
	g.mu.Lock()
	delete(g.reserved, dir)
	g.mu.Unlock()
}

// acquire blocks until need bytes are reserved for dir or ctx is cancelled,
// polling every waitPollInterval. With wait=false a single failed check
// returns ErrNoSpace immediately (side-by-side mode never blocks the queue).
func (g *SpaceGuard) acquire(ctx context.Context, dir string, need int64, wait bool) error {
	if g.reserve(dir, need) {
		return nil
	}
	if !wait {
		return fmt.Errorf("%w for %s: need %s plus what running jobs still require", ErrNoSpace, dir, humanBytes(need))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitPollInterval):
			if g.reserve(dir, need) {
				return nil
			}
		}
	}
}

// humanBytes renders n as a short IEC string for error messages.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", v)
}

// reserveOutput checks that the destination filesystem has room for the
// remuxed output on top of what the already-running jobs still need, and
// registers the reservation. In replace mode the reservation only lives
// until the atomic rename frees the original again (temporary usage), so
// acquiring space waits for other jobs to finish instead of failing; in
// side-by-side mode the output is a permanent extra file, so a full disk is
// a hard error. Returns a release func that must be called when the
// conversion ends (success or failure); nil when no guard is configured.
func (o Options) reserveOutput(ctx context.Context, srcPath string) (func(), error) {
	g := o.Space
	if g == nil {
		return func() {}, nil
	}
	st, err := os.Stat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("stat source for space check: %w", err)
	}
	dir := filepath.Dir(srcPath)
	need := int64(float64(st.Size()) * spaceHeadroom)
	if err := g.acquire(ctx, dir, need, o.Replace); err != nil {
		return nil, err
	}
	return func() { g.release(dir) }, nil
}

// accountWritten tells the space guard that n output bytes have landed on
// disk, shrinking this job's reservation so the projected final free space
// stays accurate. No-op without a configured guard.
func (o Options) accountWritten(path string, n int64) {
	if o.Space == nil {
		return
	}
	o.Space.shrink(filepath.Dir(path), n)
}
