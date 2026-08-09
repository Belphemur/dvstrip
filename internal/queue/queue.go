// Package queue is a fixed-size goroutine worker pool used to fan out the
// CPU/IO-bound ffmpeg/dovi_tool work. Submit (and its dedup guard) is safe
// for concurrent use from multiple goroutines.
package queue

import (
	"context"
	"runtime"
	"sync"
)

// Job is one file to process.
type Job struct {
	Path string
}

// Handler processes a single job. It runs inside a worker goroutine and must
// be safe for concurrent use.
type Handler func(ctx context.Context, job Job)

// Queue is a fixed worker pool fed by a buffered channel.
type Queue struct {
	workers  int
	handler  Handler
	jobs     chan Job
	inflight sync.Map // paths currently queued or running (dedup)
	pending  sync.WaitGroup
}

// New returns a Queue that will start `workers` goroutines on Start.
func New(workers int, h Handler) *Queue {
	if workers < 1 {
		workers = 1
	}
	return &Queue{
		workers: workers,
		handler: h,
		jobs:    make(chan Job, workers*8), // buffered: Submit gives natural backpressure
	}
}

// Start launches the worker goroutines.
//
// Prototype simplification: workers loop forever and exit with the process;
// the jobs channel is never closed so Wait() (pending counter) is the only
// drain mechanism. This is fine for a CLI/CLI-daemon hybrid that is meant to
// be restarted between runs.
func (q *Queue) Start(ctx context.Context) {
	for i := 0; i < q.workers; i++ {
		go func() {
			for job := range q.jobs {
				q.handler(ctx, job)
				q.inflight.Delete(job.Path)
				q.pending.Done()
			}
		}()
	}
}

// Submit enqueues a file. Returns false if it was already queued or running
// (so a file copy firing dozens of fsnotify events yields exactly one job).
func (q *Queue) Submit(j Job) bool {
	if _, loaded := q.inflight.LoadOrStore(j.Path, struct{}{}); loaded {
		return false
	}
	q.pending.Add(1)
	q.jobs <- j
	return true
}

// Wait blocks until every submitted job has finished.
func (q *Queue) Wait() { q.pending.Wait() }

// WorkerCount returns the number of configured workers (for logging).
func (q *Queue) WorkerCount() int { return q.workers }

// AutoWorkers picks the default pool size when --workers=0. Remuxing
// (stream copy) is I/O-bound and each ffmpeg process uses well under one
// core, so we take half the logical CPUs, clamped to [2, 8]: enough
// parallelism to keep disks busy without thrashing them on HDD/NAS targets.
func AutoWorkers() int {
	n := runtime.NumCPU() / 2
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}
