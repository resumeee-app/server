package api

import (
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
)

// ChannelQueue provides a generic bounded channel-backed queue for asynchronous task dispatch.
type ChannelQueue[T any] struct {
	ch chan T
}

// NewChannelQueue constructs a ChannelQueue with the specified buffer capacity.
func NewChannelQueue[T any](capacity int) *ChannelQueue[T] {
	if capacity <= 0 {
		capacity = config.DefaultWorkerQueueSize
	}
	return &ChannelQueue[T]{
		ch: make(chan T, capacity),
	}
}

// Push sends an item to the buffered channel or returns core.ErrQueueFull if saturated.
func (q *ChannelQueue[T]) Push(item T) error {
	select {
	case q.ch <- item:
		return nil
	default:
		return core.ErrQueueFull
	}
}

// Channel exposes the underlying receive channel for background consumers.
func (q *ChannelQueue[T]) Channel() <-chan T {
	return q.ch
}

// ChannelJobEnqueuer implements branch.JobEnqueuer backed by ChannelQueue[core.GenerationJob].
type ChannelJobEnqueuer struct {
	queue *ChannelQueue[core.GenerationJob]
}

// Compile-time assertion ensuring ChannelJobEnqueuer satisfies branch.JobEnqueuer.
var _ branch.JobEnqueuer = (*ChannelJobEnqueuer)(nil)

// NewChannelJobEnqueuer creates a new ChannelJobEnqueuer with the given buffer capacity.
func NewChannelJobEnqueuer(capacity int) *ChannelJobEnqueuer {
	return &ChannelJobEnqueuer{
		queue: NewChannelQueue[core.GenerationJob](capacity),
	}
}

// Enqueue sends a GenerationJob to the buffered channel or returns core.ErrQueueFull if saturated.
func (e *ChannelJobEnqueuer) Enqueue(job core.GenerationJob) error {
	return e.queue.Push(job)
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelJobEnqueuer) Channel() <-chan core.GenerationJob {
	return e.queue.Channel()
}

// ChannelPDFJobEnqueuer implements core.PDFJobEnqueuer backed by ChannelQueue[core.PDFParseJobTask].
type ChannelPDFJobEnqueuer struct {
	queue *ChannelQueue[core.PDFParseJobTask]
}

// Compile-time assertion ensuring ChannelPDFJobEnqueuer satisfies core.PDFJobEnqueuer.
var _ core.PDFJobEnqueuer = (*ChannelPDFJobEnqueuer)(nil)

// NewChannelPDFJobEnqueuer creates a new ChannelPDFJobEnqueuer with the given buffer capacity.
func NewChannelPDFJobEnqueuer(capacity int) *ChannelPDFJobEnqueuer {
	return &ChannelPDFJobEnqueuer{
		queue: NewChannelQueue[core.PDFParseJobTask](capacity),
	}
}

// EnqueuePDFJob sends a PDFParseJobTask to the buffered channel or returns core.ErrQueueFull if saturated.
func (e *ChannelPDFJobEnqueuer) EnqueuePDFJob(task core.PDFParseJobTask) error {
	return e.queue.Push(task)
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelPDFJobEnqueuer) Channel() <-chan core.PDFParseJobTask {
	return e.queue.Channel()
}
