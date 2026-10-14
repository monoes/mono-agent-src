package workflow

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ExecutionRequest is enqueued when a workflow execution is triggered.
type ExecutionRequest struct {
	WorkflowID  string
	ExecutionID string
	TriggerType string
	// TriggerNodeID identifies which trigger node fired. Empty means "all trigger
	// nodes" (manual/retry runs), preserving legacy fan-out behaviour.
	TriggerNodeID string
	TriggerData   map[string]interface{}
}

// ConcurrencySlot represents an execution's hold on the engine's bounded
// concurrency. A node that must wait a long time (e.g. Human-in-Loop) can
// Release its slot so other executions can run while it waits, then Acquire
// it again before continuing. Held for the whole execution otherwise.
type ConcurrencySlot interface {
	Release()
	Acquire(ctx context.Context) error
}

type slotCtxKey struct{}

// ContextWithSlot attaches a ConcurrencySlot to a context.
func ContextWithSlot(ctx context.Context, slot ConcurrencySlot) context.Context {
	return context.WithValue(ctx, slotCtxKey{}, slot)
}

// SlotFromContext returns the ConcurrencySlot attached to ctx, if any.
func SlotFromContext(ctx context.Context) (ConcurrencySlot, bool) {
	s, ok := ctx.Value(slotCtxKey{}).(ConcurrencySlot)
	return s, ok
}

// YieldSlotWhile runs fn with the execution's ConcurrencySlot released, so
// other executions can run while this one only waits (a retry back-off, a
// core.wait delay), then re-acquires the slot. Without a slot in ctx it just
// runs fn. fn's error wins; otherwise a failed re-acquire (ctx cancelled while
// waiting for a free slot) returns ErrExecutionCancelled.
func YieldSlotWhile(ctx context.Context, fn func() error) error {
	slot, ok := SlotFromContext(ctx)
	if !ok {
		return fn()
	}
	slot.Release()
	err := fn()
	if aerr := slot.Acquire(ctx); aerr != nil && err == nil {
		err = ErrExecutionCancelled
	}
	return err
}

// gate bounds the number of executions actively doing work via a token channel.
type gate struct {
	tokens chan struct{}
}

func newGate(n int) *gate {
	if n < 1 {
		n = 1
	}
	return &gate{tokens: make(chan struct{}, n)}
}

func (g *gate) acquire(ctx context.Context) error {
	select {
	case g.tokens <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gate) release() {
	select {
	case <-g.tokens:
	default:
	}
}

// gateSlot is a per-execution ConcurrencySlot backed by the shared gate. It
// tracks whether it currently holds a token so Release/Acquire are idempotent
// and a double release can never free another execution's token.
type gateSlot struct {
	g    *gate
	mu   sync.Mutex
	held bool
}

func (s *gateSlot) Acquire(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held {
		return nil
	}
	if err := s.g.acquire(ctx); err != nil {
		return err
	}
	s.held = true
	return nil
}

func (s *gateSlot) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.held {
		return
	}
	s.g.release()
	s.held = false
}

// ExecutionQueue buffers pending execution requests and runs each in its own
// goroutine, bounded by a concurrency gate. A single dispatcher drains the
// channel; using per-execution goroutines (rather than a fixed worker pool)
// lets a long-waiting node yield its concurrency slot without parking a worker.
type ExecutionQueue struct {
	ch          chan ExecutionRequest
	gate        *gate
	wg          sync.WaitGroup
	cancelFuncs sync.Map // executionID → context.CancelCauseFunc
	handler     func(ctx context.Context, req ExecutionRequest)
	// claim, when set, makes this process the owner of a request's row
	// before it is buffered, so a row waiting here is never adoptable.
	claim  func(executionID string)
	logger zerolog.Logger

	mu     sync.Mutex // guards closed and serialises sends against Stop's close
	closed bool
}

// NewExecutionQueue creates a queue with the given buffer capacity and a
// maximum number of concurrently-executing (non-waiting) executions.
func NewExecutionQueue(capacity int, maxConcurrent int, handler func(ctx context.Context, req ExecutionRequest), logger zerolog.Logger) *ExecutionQueue {
	return &ExecutionQueue{
		ch:      make(chan ExecutionRequest, capacity),
		gate:    newGate(maxConcurrent),
		handler: handler,
		logger:  logger,
	}
}

// Start launches the dispatcher goroutine. Must be called before Enqueue.
// The provided context governs dispatcher and execution lifetime.
func (q *ExecutionQueue) Start(ctx context.Context) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.logger.Info().Msg("workflow queue dispatcher started")
		defer q.logger.Info().Msg("workflow queue dispatcher stopped")
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-q.ch:
				if !ok {
					return // channel closed by Stop()
				}
				q.wg.Add(1)
				go q.run(ctx, req)
			}
		}
	}()
}

// run executes a single request in its own goroutine: it creates a cancellable
// child context, acquires a concurrency slot (blocking until one is free),
// attaches the slot so long-waiting nodes can yield it, runs the handler, then
// cleans up.
func (q *ExecutionQueue) run(ctx context.Context, req ExecutionRequest) {
	defer q.wg.Done()

	execCtx, cancel := context.WithCancelCause(ctx)
	q.cancelFuncs.Store(req.ExecutionID, cancel)

	slot := &gateSlot{g: q.gate}
	defer func() {
		q.cancelFuncs.Delete(req.ExecutionID)
		slot.Release()
		cancel(nil)
		if r := recover(); r != nil {
			q.logger.Error().
				Str("execution_id", req.ExecutionID).
				Str("workflow_id", req.WorkflowID).
				Interface("panic", r).
				Msg("workflow handler panicked")
		}
	}()

	if err := slot.Acquire(execCtx); err != nil {
		// Context cancelled while waiting for a slot (shutdown/cancel).
		return
	}

	q.logger.Debug().
		Str("execution_id", req.ExecutionID).
		Str("workflow_id", req.WorkflowID).
		Str("trigger_type", req.TriggerType).
		Msg("dispatching execution request")

	q.handler(ContextWithSlot(execCtx, slot), req)
}

// Stop closes the channel and waits for the dispatcher and all in-flight
// executions to exit, bounded by a 15s grace period. Callers should cancel
// the context passed to Start first so waiting executions unblock; handlers
// that ignore cancellation past the grace period are abandoned (their
// goroutines may still be running when Stop returns).
func (q *ExecutionQueue) Stop() {
	q.StopWithGrace(15 * time.Second)
}

// StopWithGrace is Stop with an explicit drain deadline.
func (q *ExecutionQueue) StopWithGrace(grace time.Duration) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.ch)
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		q.logger.Warn().
			Str("grace", grace.String()).
			Msg("workflow queue drain exceeded grace period; abandoning still-running executions")
	}
}

// Enqueue adds a request to the queue. Returns ErrQueueFull if the buffer is
// full, or ErrQueueClosed if the queue has been stopped. The closed check and
// the (non-blocking) send are serialised under mu against Stop's close, so a
// trigger firing during shutdown can never panic with send-on-closed-channel.
func (q *ExecutionQueue) Enqueue(req ExecutionRequest) error {
	if q.claim != nil {
		q.claim(req.ExecutionID)
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	select {
	case q.ch <- req:
		q.mu.Unlock()
		q.logger.Debug().
			Str("execution_id", req.ExecutionID).
			Str("workflow_id", req.WorkflowID).
			Msg("execution request enqueued")
		return nil
	default:
		q.mu.Unlock()
		q.logger.Warn().
			Str("execution_id", req.ExecutionID).
			Str("workflow_id", req.WorkflowID).
			Msg("execution queue full, request rejected")
		return ErrQueueFull
	}
}

// RunningIDs lists the executions this queue has dispatched and not seen
// finish: the ones running and the ones waiting for a concurrency slot. Each
// holds a cancel func in cancelFuncs for exactly that long.
func (q *ExecutionQueue) RunningIDs() []string {
	var ids []string
	q.cancelFuncs.Range(func(k, _ any) bool {
		if id, ok := k.(string); ok {
			ids = append(ids, id)
		}
		return true
	})
	return ids
}

// CancelWithCause cancels an execution and records why in its context, where
// context.Cause reads it. A nil cause is a plain cancel.
func (q *ExecutionQueue) CancelWithCause(executionID string, cause error) {
	if val, ok := q.cancelFuncs.Load(executionID); ok {
		if cancel, ok := val.(context.CancelCauseFunc); ok {
			cancel(cause)
			q.logger.Info().
				Str("execution_id", executionID).
				Msg("execution cancellation signalled")
		}
	}
}
