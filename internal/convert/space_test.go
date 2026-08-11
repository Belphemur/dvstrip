package convert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreeSpace(t *testing.T) {
	free, err := freeSpace(t.TempDir())
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	if free <= 0 {
		t.Errorf("free space = %d, want > 0", free)
	}
}

// reservedFor is a test helper reading the guard's total for the filesystem
// holding dir.
func reservedFor(t *testing.T, g *SpaceGuard, dir string) int64 {
	t.Helper()
	key, err := fsKey(dir)
	if err != nil {
		t.Fatalf("fsKey: %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reserved[key]
}

func TestSpaceGuardZeroValue(t *testing.T) {
	// The zero value must be ready to use: no NewSpaceGuard, no nil-map panic.
	var g SpaceGuard
	dir := t.TempDir()
	r := g.reserve(dir, 1024)
	if r == nil {
		t.Fatal("reserve on zero-value guard failed")
	}
	g.shrinkDir(dir, 512)
	g.release(r)
	if got := reservedFor(t, &g, dir); got != 0 {
		t.Errorf("reserved after release = %d, want 0", got)
	}
}

func TestSpaceGuardReserveAccounting(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}

	// A reservation beyond free space must fail and leave nothing reserved.
	if g.reserve(dir, free*2) != nil {
		t.Fatal("oversized reservation unexpectedly succeeded")
	}
	r := g.reserve(dir, free/2)
	if r == nil {
		t.Fatal("reservation within free space failed")
	}
	// The projected final space left counts running jobs: a second job must
	// not be able to push the total over the free space.
	if g.reserve(dir, free) != nil {
		t.Fatal("reservation exceeding free-minus-reserved succeeded")
	}
	// As bytes land on disk the reservation shrinks, but the file itself now
	// occupies the space — the projection must stay conservative.
	g.shrinkDir(dir, free/4)
	if g.reserve(dir, free) != nil {
		t.Fatal("reservation beyond projected final space succeeded after shrink")
	}
	g.release(r)
	if g.reserve(dir, free/2) == nil {
		t.Fatal("reservation after release failed")
	}
	// Shrinking past zero must clamp, not go negative.
	g.shrinkDir(dir, free*10)
	if got := reservedFor(t, g, dir); got != 0 {
		t.Errorf("reservation after overshrink = %d, want 0", got)
	}
}

// TestSpaceGuardSharedFilesystem: sibling directories on the same filesystem
// must share one ledger entry — a reservation made in one must count against
// the free space seen from the other.
func TestSpaceGuardSharedFilesystem(t *testing.T) {
	parent := t.TempDir()
	a := filepath.Join(parent, "a")
	b := filepath.Join(parent, "b")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ka, err := fsKey(a)
	if err != nil {
		t.Fatalf("fsKey(a): %v", err)
	}
	kb, err := fsKey(b)
	if err != nil {
		t.Fatalf("fsKey(b): %v", err)
	}
	if ka != kb {
		t.Skip("sibling temp dirs landed on different filesystems")
	}

	g := NewSpaceGuard()
	free, err := freeSpace(a)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	r := g.reserve(a, free/2)
	if r == nil {
		t.Fatal("reserve on dir a failed")
	}
	// From b's point of view the same bytes are already spoken for.
	if g.reserve(b, free) != nil {
		t.Fatal("reservation from sibling dir ignored the existing reservation")
	}
	g.release(r)
	if g.reserve(b, free/2) == nil {
		t.Fatal("reservation from sibling dir failed after release")
	}
}

func TestSpaceGuardAcquireNoWait(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	_, err = g.acquire(context.Background(), dir, free*2, false)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("acquire error = %v, want ErrNoSpace", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the directory: %v", err)
	}
}

// shortPoll temporarily shortens the replace-mode poll interval for tests.
func shortPoll(t *testing.T) {
	t.Helper()
	orig := waitPollInterval
	waitPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { waitPollInterval = orig })
}

func TestSpaceGuardAcquireWaitUnblockedByRelease(t *testing.T) {
	shortPoll(t)
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	r := g.reserve(dir, free)
	if r == nil {
		t.Fatal("initial full-disk reservation failed")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		g.release(r)
	}()
	start := time.Now()
	if _, err := g.acquire(context.Background(), dir, free/2, true); err != nil {
		t.Fatalf("acquire(wait) = %v", err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("acquire(wait) returned before the release it was waiting for")
	}
}

func TestSpaceGuardAcquireWaitCancelled(t *testing.T) {
	shortPoll(t)
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	if g.reserve(dir, free) == nil {
		t.Fatal("initial full-disk reservation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.acquire(ctx, dir, 1, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire(wait) on full disk = %v, want context deadline", err)
	}
}

func TestReserveOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}

	// Guard disabled: reservation is a no-op and never errors.
	release, err := (Options{}).reserveOutput(context.Background(), src)
	if err != nil {
		t.Fatalf("reserveOutput without guard: %v", err)
	}
	release()

	// A 1 KiB file easily fits: both modes succeed.
	for _, replace := range []bool{false, true} {
		release, err := (Options{Replace: replace, Space: NewSpaceGuard()}).reserveOutput(context.Background(), src)
		if err != nil {
			t.Fatalf("reserveOutput(replace=%v): %v", replace, err)
		}
		release()
	}

	// Missing source errors when a guard is active (the stat is part of the
	// space check).
	if _, err := (Options{Space: NewSpaceGuard()}).reserveOutput(context.Background(), filepath.Join(dir, "gone.mkv")); err == nil {
		t.Error("reserveOutput on missing source: expected error")
	}
}

func TestAccountWritten(t *testing.T) {
	// No guard: no-op, must not panic.
	(Options{}).accountWritten("/x/movie.mkv", 100)

	dir := t.TempDir()
	g := NewSpaceGuard()
	if g.reserve(dir, 1000) == nil {
		t.Fatal("reserve failed")
	}
	(Options{Space: g}).accountWritten(filepath.Join(dir, "movie.mkv"), 400)
	if got := reservedFor(t, g, dir); got != 600 {
		t.Errorf("reservation after accountWritten = %d, want 600", got)
	}
}

// TestAccountWrittenOutsideReservation: bytes landing outside the reserved
// destination (the P5 intermediates under os.MkdirTemp) must not shrink the
// reservation.
func TestAccountWrittenOutsideReservation(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	if g.reserve(dir, 1000) == nil {
		t.Fatal("reserve failed")
	}
	tmp := t.TempDir() // stands in for the os.MkdirTemp P5 dir
	(Options{Space: g}).accountWritten(filepath.Join(tmp, "bl.hevc"), 400)
	if got := reservedFor(t, g, dir); got != 1000 {
		t.Errorf("reservation after foreign accountWritten = %d, want 1000", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		512:           "512 B",
		2048:          "2.0 KiB",
		5 * 1 << 20:   "5.0 MiB",
		3 * 1 << 30:   "3.0 GiB",
		1<<40 + 1<<39: "1.5 TiB",
		1 << 50:       "1.0 PiB",
		1 << 60:       "1.0 EiB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
