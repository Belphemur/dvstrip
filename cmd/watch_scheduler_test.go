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

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not submit within timeout")
	}

	if calls.Load() != 1 {
		t.Fatalf("submissions = %d, want 1", calls.Load())
	}
}

func TestFileSchedulerDropsRemovedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var calls atomic.Int64
	s := newFileScheduler(50*time.Millisecond, func(string) { calls.Add(1) })

	s.schedule(path)
	if s.pendingCount() != 1 {
		t.Fatalf("pending = %d, want 1", s.pendingCount())
	}

	// Removing (or renaming away) the file during the debounce window must
	// drop the entry without submitting the stale path.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("submissions = %d, want 0", calls.Load())
	}
	if s.pendingCount() != 0 {
		t.Fatalf("pending after removal = %d, want 0", s.pendingCount())
	}
}

func TestFileSchedulerIgnoresStaleTimer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var mu sync.Mutex
	var submitted []string
	s := newFileScheduler(40*time.Millisecond, func(p string) {
		mu.Lock()
		submitted = append(submitted, p)
		mu.Unlock()
	})

	// Simulate an AfterFunc callback that fired before Stop ran but only
	// gets to act after the file was rescheduled: the stale generation must
	// not delete the entry nor submit early.
	s.schedule(path)
	s.mu.Lock()
	p := s.timers[path]
	staleGen := p.gen
	s.mu.Unlock()
	s.schedule(path)
	s.checkAndSubmit(path, p, staleGen)

	if s.pendingCount() != 1 {
		t.Fatalf("pending after stale callback = %d, want 1", s.pendingCount())
	}

	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(submitted) != 1 {
		t.Fatalf("submissions = %d, want 1", len(submitted))
	}
}

func TestFileSchedulerCloseFlushesPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var calls atomic.Int64
	s := newFileScheduler(time.Hour, func(string) { calls.Add(1) })
	s.schedule(path)
	if s.pendingCount() != 1 {
		t.Fatalf("pending = %d, want 1", s.pendingCount())
	}

	// Shutdown must not wait for the debounce window: pending files are
	// submitted immediately, exactly once.
	s.Close()
	if calls.Load() != 1 {
		t.Fatalf("submissions after Close = %d, want 1", calls.Load())
	}
	if s.pendingCount() != 0 {
		t.Fatalf("pending after Close = %d, want 0", s.pendingCount())
	}

	// The stopped timer must never fire a second submission.
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("submissions after timer window = %d, want 1", calls.Load())
	}
}

func TestFileSchedulerScheduleDirSchedulesEligibleFiles(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o600); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write text: %v", err)
	}
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nested := filepath.Join(sub, "other.mp4")
	if err := os.WriteFile(nested, []byte("x"), 0o600); err != nil {
		t.Fatalf("write nested video: %v", err)
	}

	var mu sync.Mutex
	seen := map[string]int{}
	s := newFileScheduler(40*time.Millisecond, func(p string) {
		mu.Lock()
		seen[p]++
		mu.Unlock()
	})

	var walkErr atomic.Int64
	s.scheduleDir(dir, func(error) { walkErr.Add(1) })

	if walkErr.Load() != 0 {
		t.Fatalf("walk errors = %d, want 0", walkErr.Load())
	}
	if s.pendingCount() != 2 {
		t.Fatalf("pending = %d, want 2 (only the videos)", s.pendingCount())
	}

	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if seen[video] != 1 || seen[nested] != 1 || len(seen) != 2 {
		t.Fatalf("submissions = %v, want exactly %s and %s once each", seen, video, nested)
	}
}

func TestFileSchedulerScheduleDirReportsWalkErrors(t *testing.T) {
	s := newFileScheduler(time.Millisecond, func(string) {})

	var gotErr atomic.Int64
	s.scheduleDir(filepath.Join(t.TempDir(), "does-not-exist"), func(error) { gotErr.Add(1) })
	if gotErr.Load() != 1 {
		t.Fatalf("onError calls = %d, want 1", gotErr.Load())
	}
}
