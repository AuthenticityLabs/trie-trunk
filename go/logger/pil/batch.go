package pil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A queued caller still waits for the server acknowledgement. Concurrent writes
// share a deadline measured from the first write; later arrivals never reset it.
// No event-count threshold controls emission.
type batchWrite struct {
	ctx    context.Context
	branch string
	frames [][]byte
	done   chan error
}

type timedBatch struct {
	mu      sync.Mutex
	send    sync.Mutex
	queue   []*batchWrite
	bytes   int
	timer   *time.Timer
	closed  bool
	lastErr error
}

func (w *Logger) enqueue(ctx context.Context, branch string, frames [][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b := &w.batch
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("PIL logger closed")
	}
	size := 0
	for _, frame := range frames {
		if len(frame) > (64<<20)-b.bytes-size {
			b.mu.Unlock()
			return fmt.Errorf("PIL batch queue is full")
		}
		size += len(frame)
	}
	// Capacity is backpressure, never an alternative batching trigger.
	job := &batchWrite{ctx: ctx, branch: branch, done: make(chan error, 1)}
	for _, frame := range frames {
		job.frames = append(job.frames, append([]byte(nil), frame...))
	}
	b.queue = append(b.queue, job)
	b.bytes += size
	if b.timer == nil {
		b.timer = time.AfterFunc(time.Duration(w.config.BatchMilliseconds)*time.Millisecond, func() {
			_ = w.Flush(context.Background())
		})
	}
	b.mu.Unlock()
	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush ends the current linger interval and waits for queued delivery. It does
// not retry an ambiguous delivery. The original callers receive their own errors.
func (w *Logger) Flush(ctx context.Context) error {
	b := &w.batch
	b.send.Lock()
	defer b.send.Unlock()
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	queue := b.queue
	b.queue = nil
	b.bytes = 0
	b.mu.Unlock()
	var result error
	for start := 0; start < len(queue); {
		end := start + 1
		for end < len(queue) && queue[end].branch == queue[start].branch {
			end++
		}
		var frames [][]byte
		var jobs []*batchWrite
		for _, job := range queue[start:end] {
			if err := job.ctx.Err(); err != nil {
				job.done <- err
				continue
			}
			jobs = append(jobs, job)
			frames = append(frames, job.frames...)
		}
		if len(jobs) != 0 {
			err := w.writeFramesNow(ctx, frames, queue[start].branch)
			result = errors.Join(result, err)
			for _, job := range jobs {
				job.done <- err
			}
		}
		start = end
	}
	b.mu.Lock()
	b.lastErr = errors.Join(b.lastErr, result)
	b.mu.Unlock()
	return result
}
