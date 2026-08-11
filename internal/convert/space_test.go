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

func TestSpaceGuardReserveAccounting(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}

	// A reservation beyond free space must fail and leave nothing reserved.
	if g.reserve(dir, free*2) {
		t.Fatal("oversized reservation unexpectedly succeeded")
	}
	if !g.reserve(dir, free/2) {
		t.Fatal("reservation within free space failed")
	}
	// The projected final space left counts running jobs: a second job must
	// not be able to push the total over the free space.
	if g.reserve(dir, free) {
		t.Fatal("reservation exceeding free-minus-reserved succeeded")
	}
	// As bytes land on disk the reservation shrinks, but the file itself now
	// occupies the space — the projection must stay conservative.
	g.shrink(dir, free/4)
	if g.reserve(dir, free) {
		t.Fatal("reservation beyond projected final space succeeded after shrink")
	}
	g.release(dir)
	if !g.reserve(dir, free/2) {
		t.Fatal("reservation after release failed")
	}
	// Shrinking past zero must clamp, not go negative.
	g.shrink(dir, free*10)
	g.mu.Lock()
	r := g.reserved[dir]
	g.mu.Unlock()
	if r != 0 {
		t.Errorf("reservation after overshrink = %d, want 0", r)
	}
}

func TestSpaceGuardAcquireNoWait(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	err = g.acquire(context.Background(), dir, free*2, false)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("acquire error = %v, want ErrNoSpace", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the directory: %v", err)
	}
}

func TestSpaceGuardAcquireWaitUnblockedByRelease(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	if !g.reserve(dir, free) {
		t.Fatal("initial full-disk reservation failed")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		g.release(dir)
	}()
	start := time.Now()
	if err := g.acquire(context.Background(), dir, free/2, true); err != nil {
		t.Fatalf("acquire(wait) = %v", err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("acquire(wait) returned before the release it was waiting for")
	}
}

func TestSpaceGuardAcquireWaitCancelled(t *testing.T) {
	dir := t.TempDir()
	g := NewSpaceGuard()
	free, err := freeSpace(dir)
	if err != nil {
		t.Fatalf("freeSpace: %v", err)
	}
	if !g.reserve(dir, free) {
		t.Fatal("initial full-disk reservation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := g.acquire(ctx, dir, 1, true); !errors.Is(err, context.DeadlineExceeded) {
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
	if !g.reserve(dir, 1000) {
		t.Fatal("reserve failed")
	}
	(Options{Space: g}).accountWritten(filepath.Join(dir, "movie.mkv"), 400)
	g.mu.Lock()
	r := g.reserved[dir]
	g.mu.Unlock()
	if r != 600 {
		t.Errorf("reservation after accountWritten = %d, want 600", r)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		512:           "512 B",
		2048:          "2.0 KiB",
		5 * 1 << 20:   "5.0 MiB",
		3 * 1 << 30:   "3.0 GiB",
		1<<40 + 1<<39: "1.5 TiB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
