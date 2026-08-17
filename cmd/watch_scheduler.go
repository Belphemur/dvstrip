package cmd

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Belphemur/dvstrip/internal/queue"
)

// fileScheduler debounces fsnotify events and waits for a file's size to
// settle before submitting it to the worker queue. This is important for
// hard-linked files: the Create event may arrive while the shared inode is
// still being written in another directory, so probing immediately would see
// a partial file. The scheduler keeps rescheduling until the size stays
// constant for the configured debounce interval.
type fileScheduler struct {
	mu     sync.Mutex
	delay  time.Duration
	timers map[string]*pendingFile
	submit func(string)
}

// pendingFile tracks the state of one file waiting to settle. gen increments
// on every (re)arm; timer callbacks carry the generation that armed them so
// stale callbacks — time.Timer.Stop cannot withdraw an already-fired
// AfterFunc callback — are recognized and ignored.
type pendingFile struct {
	timer *time.Timer
	size  int64
	gen   uint64
}

// newFileScheduler returns a scheduler that calls submit after a file has
// stopped changing for delay.
func newFileScheduler(delay time.Duration, submit func(string)) *fileScheduler {
	return &fileScheduler{
		delay:  delay,
		timers: map[string]*pendingFile{},
		submit: submit,
	}
}

// schedule records an event for path. The file will be submitted once its
// size has remained stable for the configured debounce interval.
func (s *fileScheduler) schedule(path string) {
	pkg.Debug().Str("file", filepath.Base(path)).Msg("scheduler.schedule called")
	s.mu.Lock()
	defer s.mu.Unlock()

	curSize := int64(-1)
	if st, err := os.Stat(path); err == nil {
		curSize = st.Size()
	} else if err != nil {
		pkg.Debug().Str("file", filepath.Base(path)).Err(err).Msg("scheduler.schedule: stat failed")
	}

	p, ok := s.timers[path]
	if ok {
		p.timer.Stop()
		p.size = curSize
	} else {
		p = &pendingFile{size: curSize}
		s.timers[path] = p
	}
	s.armLocked(path, p)
}

// armLocked starts a fresh settle timer for an already-pending file.
// Callers must hold s.mu.
func (s *fileScheduler) armLocked(path string, p *pendingFile) {
	p.gen++
	gen := p.gen
	p.timer = time.AfterFunc(s.delay, func() {
		s.checkAndSubmit(path, p, gen)
	})
}

// checkAndSubmit verifies the file size has not changed since the timer was
// set. If it changed, it resets the timer; otherwise it submits the path.
// A callback whose generation has been superseded (newer event, retry, or a
// Close flush) is stale and must not act. A file that no longer exists
// (removed or renamed away) is dropped without submitting; transient Stat
// errors never count as a stable size.
func (s *fileScheduler) checkAndSubmit(path string, p *pendingFile, gen uint64) {
	pkg.Debug().Str("file", filepath.Base(path)).Uint64("gen", gen).Int64("stored_size", p.size).Msg("scheduler.checkAndSubmit")
	s.mu.Lock()
	cur, ok := s.timers[path]
	if !ok || cur != p || p.gen != gen {
		if !ok {
			pkg.Debug().Str("file", filepath.Base(path)).Msg("checkAndSubmit: path removed from timers")
		} else if cur != p {
			pkg.Debug().Str("file", filepath.Base(path)).Msg("checkAndSubmit: stale entry (replacement)")
		} else {
			pkg.Debug().Str("file", filepath.Base(path)).Uint64("callback_gen", gen).Uint64("current_gen", p.gen).Msg("checkAndSubmit: stale generation — superseded")
		}
		s.mu.Unlock()
		return
	}
	st, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		pkg.Debug().Str("file", filepath.Base(path)).Msg("checkAndSubmit: file no longer exists, dropping")
		delete(s.timers, path)
		s.mu.Unlock()
		return
	case err != nil:
		// Transient I/O errors must not count as a stable size: retry.
		pkg.Debug().Str("file", filepath.Base(path)).Err(err).Msg("checkAndSubmit: stat error, retrying")
		s.armLocked(path, p)
		s.mu.Unlock()
		return
	}
	if st.Size() != p.size {
		pkg.Debug().Str("file", filepath.Base(path)).Int64("old_size", p.size).Int64("new_size", st.Size()).Msg("checkAndSubmit: size changed, re-arming")
		p.size = st.Size()
		s.armLocked(path, p)
		s.mu.Unlock()
		return
	}
	pkg.Debug().Str("file", filepath.Base(path)).Int64("size", st.Size()).Msg("checkAndSubmit: size stable, submitting")
	delete(s.timers, path)
	s.mu.Unlock()
	s.submit(path)
}

// Close flushes every pending file: it stops the debounce timers and submits
// the current paths immediately, so nothing scheduled is lost on shutdown.
func (s *fileScheduler) Close() {
	s.mu.Lock()
	pending := s.timers
	s.timers = map[string]*pendingFile{}
	for _, p := range pending {
		p.timer.Stop()
	}
	s.mu.Unlock()

	pkg.Info().Int("count", len(pending)).Msg("scheduler.Close: flushing pending files")
	for path := range pending {
		s.submit(path)
	}
}

// pendingCount returns the number of files currently waiting to settle.
// It is used by tests.
func (s *fileScheduler) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

// scheduleDir walks dir with the same eligibility rules as submitDir but
// routes every eligible file through the scheduler instead of the queue, so
// files found in a freshly created directory also wait for their size to
// settle. Walk failures are reported to onError so they are not silently
// dropped.
func (s *fileScheduler) scheduleDir(dir string, onError func(error)) {
	pkg.Debug().Str("dir", dir).Msg("scheduler.scheduleDir called")
	if err := walkVideos(dir, s.schedule); err != nil && onError != nil {
		onError(err)
	}
}

// queueSubmit adapts fileScheduler's submit callback to queue.Submit.
func queueSubmit(q *queue.Queue) func(string) {
	return func(path string) {
		pkg.Debug().Str("file", filepath.Base(path)).Msg("scheduler submitted to queue")
		q.Submit(queue.Job{Path: path})
	}
}
