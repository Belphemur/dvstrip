package cmd

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileSchedulerWaitsForSizeToSettle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("ab"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var submitted atomic.Int64
	s := newFileScheduler(200*time.Millisecond, func(p string) {
		if p == path {
			submitted.Add(1)
		}
	})

	// First event starts the timer for a 2-byte file.
	s.schedule(path)
	if s.pendingCount() != 1 {
		t.Fatalf("pending = %d, want 1", s.pendingCount())
	}

	// While the timer is running, grow the file and schedule again. The
	// scheduler should notice the size changed and reset instead of submitting.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
		t.Fatalf("grow file: %v", err)
	}
	s.schedule(path)

	// Wait long enough that the original timer would have fired (it was
	// stopped by the second schedule), but not long enough for the reset timer
	// to fire.
	time.Sleep(100 * time.Millisecond)
	if submitted.Load() != 0 {
		t.Fatalf("submitted before settle: %d", submitted.Load())
	}
	if s.pendingCount() != 1 {
		t.Fatalf("pending before settle = %d, want 1", s.pendingCount())
	}

	// Now leave the file alone. After the debounce it should be submitted.
	time.Sleep(300 * time.Millisecond)
	if submitted.Load() != 1 {
		t.Fatalf("submitted = %d, want 1", submitted.Load())
	}
	if s.pendingCount() != 0 {
		t.Fatalf("pending after submit = %d, want 0", s.pendingCount())
	}
}

func TestFileSchedulerDebouncesBurstEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var calls atomic.Int64
	s := newFileScheduler(50*time.Millisecond, func(p string) {
		if p == path {
			calls.Add(1)
		}
	})

	// Fire many events in rapid succession; only one submission should happen.
	for range 20 {
		s.schedule(path)
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("submissions = %d, want 1", calls.Load())
	}
}

func TestFileSchedulerConcurrentEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var wg sync.WaitGroup
	var calls atomic.Int64
	s := newFileScheduler(50*time.Millisecond, func(p string) {
		if p == path {
			calls.Add(1)
		}
		wg.Done()
	})

	wg.Add(1)
	for range 50 {
		go s.schedule(path)
	}
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("submissions = %d, want 1", calls.Load())
	}
}
