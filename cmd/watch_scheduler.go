package cmd

import (
	"os"
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

// pendingFile tracks the state of one file waiting to settle.
type pendingFile struct {
	timer *time.Timer
	size  int64
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
	s.mu.Lock()
	defer s.mu.Unlock()

	curSize := int64(-1)
	if st, err := os.Stat(path); err == nil {
		curSize = st.Size()
	}

	p, ok := s.timers[path]
	if ok {
		p.timer.Stop()
		p.size = curSize
	} else {
		p = &pendingFile{size: curSize}
		s.timers[path] = p
	}

	p.timer = time.AfterFunc(s.delay, func() {
		s.checkAndSubmit(path)
	})
}

// checkAndSubmit verifies the file size has not changed since the timer was
// set. If it changed, it resets the timer; otherwise it submits the path.
func (s *fileScheduler) checkAndSubmit(path string) {
	s.mu.Lock()
	p, ok := s.timers[path]
	if !ok {
		s.mu.Unlock()
		return
	}
	nowSize := int64(-1)
	if st, err := os.Stat(path); err == nil {
		nowSize = st.Size()
	}
	if nowSize != p.size {
		p.size = nowSize
		p.timer = time.AfterFunc(s.delay, func() {
			s.checkAndSubmit(path)
		})
		s.mu.Unlock()
		return
	}
	delete(s.timers, path)
	s.mu.Unlock()
	s.submit(path)
}

// pendingCount returns the number of files currently waiting to settle.
// It is used by tests.
func (s *fileScheduler) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

// queueSubmit adapts fileScheduler's submit callback to queue.Submit.
func queueSubmit(q *queue.Queue) func(string) {
	return func(path string) {
		q.Submit(queue.Job{Path: path})
	}
}
