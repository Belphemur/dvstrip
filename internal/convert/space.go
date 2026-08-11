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
// enough to keep statfs spam negligible. A variable so tests can shorten it.
var waitPollInterval = 5 * time.Second

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
	mu sync.Mutex
	// reserved totals the bytes running jobs still need per filesystem key.
	// Keyed by filesystem identity (statfs Fsid), not directory path, so
	// sibling directories on the same filesystem share one ledger entry.
	reserved map[string]int64
	// jobs tracks each reservation individually per filesystem key, so
	// releasing a finished job drops only its own remaining bytes instead of
	// wiping the reservations other jobs in the same directory still hold.
	jobs map[string]map[*reservation]int64
}

// reservation identifies one acquire() so the job can shrink and release its
// own bytes without touching other jobs' reservations on the same filesystem.
// dir attributes written bytes to the job whose destination they landed in.
// Ownership stays inside SpaceGuard — callers only hold it via closure.
type reservation struct {
	key string
	dir string
}

// NewSpaceGuard returns a ready-to-use guard.
func NewSpaceGuard() *SpaceGuard {
	return &SpaceGuard{
		reserved: map[string]int64{},
		jobs:     map[string]map[*reservation]int64{},
	}
}

// freeSpace returns the bytes available to unprivileged processes on the
// filesystem holding path.
func freeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec // bavail*bsize fits int64 on every supported platform
}

// fsKey returns the identity of the filesystem holding path — the statfs
// Fsid pair — so directories sharing a mount share one ledger entry.
func fsKey(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x", uint64(st.Fsid.X__val[0]), uint64(st.Fsid.X__val[1])), nil
}

// reserve registers need extra bytes for dir, succeeding only while the
// projected space left after every running job finishes stays non-negative —
// that projection is the "clear view of the final space left" that keeps
// parallel jobs from overrunning the disk. On failure nothing is reserved
// and a nil reservation is returned.
func (g *SpaceGuard) reserve(dir string, need int64) *reservation {
	free, err := freeSpace(dir)
	if err != nil {
		// Unreadable/odd mount: don't block the conversion on a statfs
		// failure; ffmpeg failing with ENOSPC is reported like any other
		// conversion error.
		return &reservation{dir: dir}
	}
	key, err := fsKey(dir)
	if err != nil {
		return &reservation{dir: dir}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reserved == nil {
		g.reserved = map[string]int64{}
	}
	if g.jobs == nil {
		g.jobs = map[string]map[*reservation]int64{}
	}
	if free-g.reserved[key] < need {
		return nil
	}
	r := &reservation{key: key, dir: dir}
	g.reserved[key] += need
	if g.jobs[key] == nil {
		g.jobs[key] = map[*reservation]int64{}
	}
	g.jobs[key][r] = need
	return r
}

// release drops the reservation's remaining bytes (conversion done or
// aborted), leaving other jobs' reservations on the same filesystem intact.
func (g *SpaceGuard) release(r *reservation) {
	if r == nil || r.key == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	left, ok := g.jobs[r.key][r]
	if !ok {
		return
	}
	delete(g.jobs[r.key], r)
	if len(g.jobs[r.key]) == 0 {
		delete(g.jobs, r.key)
	}
	g.reserved[r.key] -= left
	if g.reserved[r.key] <= 0 {
		delete(g.reserved, r.key)
	}
}

// acquire blocks until need bytes are reserved for dir or ctx is cancelled,
// polling every waitPollInterval. With wait=false a single failed check
// returns ErrNoSpace immediately (side-by-side mode never blocks the queue).
func (g *SpaceGuard) acquire(ctx context.Context, dir string, need int64, wait bool) (*reservation, error) {
	if r := g.reserve(dir, need); r != nil {
		return r, nil
	}
	if !wait {
		return nil, fmt.Errorf("%w for %s: need %s plus what running jobs still require", ErrNoSpace, dir, humanBytes(need))
	}
	t := time.NewTimer(waitPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			if r := g.reserve(dir, need); r != nil {
				return r, nil
			}
			t.Reset(waitPollInterval)
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
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f EiB", v/unit)
}

// reserveOutput checks that the destination filesystem has room for the
// remuxed output on top of what the already-running jobs still need, and
// registers the reservation. In replace mode the reservation only lives
// until the atomic rename frees the original again (temporary usage), so
// acquiring space waits for other jobs to finish instead of failing; in
// side-by-side mode the output is a permanent extra file, so a full disk is
// a hard error. Returns a release func that must be called when the
// conversion ends (success or failure); a non-nil no-op when no guard is
// configured, so callers can always defer it unconditionally.
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
	r, err := g.acquire(ctx, dir, need, o.Replace)
	if err != nil {
		return nil, err
	}
	return func() { g.release(r) }, nil
}

// accountWritten tells the space guard that n output bytes have landed on
// disk for the file at path, shrinking that job's reservation so the
// projected final free space stays accurate for other jobs waiting to start.
// Bytes written outside the reserved destination (e.g. the P5 intermediate
// streams under os.MkdirTemp) must not be reported here. No-op without a
// configured guard.
func (o Options) accountWritten(path string, n int64) {
	if o.Space == nil || n <= 0 {
		return
	}
	o.Space.shrinkDir(filepath.Dir(path), n)
}

// shrinkDir subtracts n from the active reservations holding dir as their
// destination, keeping both the per-job remainders and the per-filesystem
// total accurate. Writes outside any reserved directory shrink nothing.
func (g *SpaceGuard) shrinkDir(dir string, n int64) {
	key, err := fsKey(dir)
	if err != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	remaining := n
	for r, left := range g.jobs[key] {
		if r.dir != dir {
			continue
		}
		d := min(left, remaining)
		g.jobs[key][r] = left - d
		g.reserved[key] -= d
		if remaining -= d; remaining == 0 {
			break
		}
	}
	if g.reserved[key] < 0 {
		g.reserved[key] = 0
	}
}
