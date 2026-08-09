package queue

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutoWorkers(t *testing.T) {
	w := AutoWorkers()
	if w < 2 || w > 8 {
		t.Errorf("AutoWorkers() = %d, want in [2,8]", w)
	}
}

func TestDedup(t *testing.T) {
	var calls atomic.Int64
	q := New(1, func(_ context.Context, j Job) {
		calls.Add(1)
		time.Sleep(5 * time.Millisecond)
	})
	q.Start(context.Background())

	j := Job{Path: "/dup.mkv"}
	// Same path submitted several times concurrently — only one should land.
	for range 5 {
		q.Submit(j)
	}
	// A second distinct path goes through.
	q.Submit(Job{Path: "/other.mkv"})
	q.Wait()
	if calls.Load() != 2 {
		t.Errorf("handler calls = %d, want 2", calls.Load())
	}
}

func TestWaitDrainsAll(t *testing.T) {
	var done atomic.Int64
	q := New(4, func(_ context.Context, j Job) {
		done.Add(1)
	})
	q.Start(context.Background())

	// distinct paths so dedup never collapses them.
	for i := range 50 {
		q.Submit(Job{Path: fmt.Sprintf("file_%d.mkv", i)})
	}
	q.Wait()
	if done.Load() != 50 {
		t.Errorf("done = %d, want 50", done.Load())
	}
}

func TestDedupCollapsesDuplicates(t *testing.T) {
	var done atomic.Int64
	q := New(1, func(_ context.Context, j Job) {
		done.Add(1)
		time.Sleep(5 * time.Millisecond) // keep the path in-flight long enough to collide
	})
	q.Start(context.Background())

	for range 50 {
		q.Submit(Job{Path: "same.mkv"})
	}
	q.Wait()
	if done.Load() != 1 {
		t.Errorf("done = %d, want 1 (50 submissions share one path)", done.Load())
	}
}
