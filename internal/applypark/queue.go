package applypark

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Runner is what the queue needs from the application commands: start one
// fill, wait for its task, record its result. Each fill is still the confined
// application-apply task; the queue only decides when to start it.
type Runner interface {
	// Start submits the application's fill task and returns the task id.
	Start(ctx context.Context, appID string) (taskID string, err error)
	// Wait blocks until the task is over (complete, failed, cancelled or
	// needs_user) and returns its final status.
	Wait(ctx context.Context, taskID string) (status string, err error)
	// Finalize records the finished task on the application (fill record,
	// awaiting_submit or needs_you, the tab binding). A task that ended
	// needs_user, with no result, becomes needs_you with a reason.
	Finalize(ctx context.Context, appID string) error
	// NeedsYou marks an application whose task ended without a usable result
	// needs_you, with the reason, so it does not stay "filling".
	NeedsYou(ctx context.Context, appID, reason string) error
	// Sweep releases the slots of tabs the person closed or finished with.
	Sweep(ctx context.Context) error
}

// Canceller is what a Runner may add to stop a fill's task when the queue gives up waiting for it.
type Canceller interface {
	Cancel(ctx context.Context, taskID string) error
}

// DefaultFillTimeout is how long the queue waits for one fill's task before it cancels it and
// leaves the application needs_you: a hung agent must not hold a slot for ever.
const DefaultFillTimeout = 20 * time.Minute

// Outcome of one application in the queue.
type Outcome struct {
	ApplicationID string `json:"applicationId"`
	TaskID        string `json:"taskId,omitempty"`
	// Result: parked (filled, tab left open), needs_you, failed or not_started.
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Outcomes of a fill.
const (
	ResultParked     = "parked"
	ResultNeedsYou   = "needs_you"
	ResultFailed     = "failed"
	ResultNotStarted = "not_started"
)

// Queue starts fills one after another, never more than Max at once counting
// the tabs already waiting on the person (parked) plus the fills running.
// It is a foreground loop: it ends when every application has been tried, or
// when ctx ends (then the applications not yet started are reported
// not_started). It never runs unattended.
type Queue struct {
	Store Store
	// Slots reserves the cap across every queue process of the profile.
	Slots  Slots
	Max    int
	Runner Runner
	// FillTimeout is the ceiling on one fill's task (0: DefaultFillTimeout).
	FillTimeout time.Duration
	// Tick is how often a full queue looks again for a freed slot.
	Tick time.Duration
	// Log, when set, hears one line per step.
	Log func(format string, a ...any)
	// Status reads the application's status after a fill, to tell parked from needs_you.
	Status func(ctx context.Context, appID string) (string, error)
}

func (q Queue) logf(format string, a ...any) {
	if q.Log != nil {
		q.Log(format, a...)
	}
}

// needsYou moves a fill that ended without a result out of "filling"; a
// failure to do so is added to the outcome's error.
func (q Queue) needsYou(ctx context.Context, write *sync.Mutex, o *Outcome, reason string) {
	write.Lock()
	err := q.Runner.NeedsYou(context.WithoutCancel(ctx), o.ApplicationID, reason)
	write.Unlock()
	if err != nil {
		o.Error += " (marking needs_you: " + err.Error() + ")"
	}
}

// Run works through ids in order and reports an Outcome for each.
func (q Queue) Run(ctx context.Context, ids []string) ([]Outcome, error) {
	max := ClampMax(q.Max)
	tick := q.Tick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	var (
		mu       sync.Mutex
		running  = map[string]bool{}
		outcomes = map[string]Outcome{}
		wg       sync.WaitGroup
		wake     = make(chan struct{}, 1)
		// write serialises what writes the database: SQLite refuses a
		// transaction that reads and then writes while another writer commits.
		write sync.Mutex
	)
	signal := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	finish := func(o Outcome) {
		// The slot is given back before anyone is woken: a fill that parked now
		// holds its slot as a tab, one that did not holds none.
		write.Lock()
		_ = q.Slots.Release(context.Background(), o.ApplicationID)
		write.Unlock()
		mu.Lock()
		outcomes[o.ApplicationID] = o
		delete(running, o.ApplicationID)
		mu.Unlock()
		signal()
	}
	runOne := func(id string) {
		defer wg.Done()
		o := Outcome{ApplicationID: id, Result: ResultFailed}
		write.Lock()
		taskID, err := q.Runner.Start(ctx, id)
		write.Unlock()
		if err != nil {
			o.Error = err.Error()
			finish(o)
			return
		}
		o.TaskID = taskID
		timeout := q.FillTimeout
		if timeout <= 0 {
			timeout = DefaultFillTimeout
		}
		wctx, stop := context.WithTimeout(ctx, timeout)
		status, err := q.Runner.Wait(wctx, taskID)
		stop()
		if err != nil && ctx.Err() == nil && wctx.Err() != nil {
			// The ceiling, not the operator's Ctrl-C: stop the task and hand the application back.
			o.Result = ResultNeedsYou
			o.Error = fmt.Sprintf("the fill ran longer than %s", timeout)
			if c, ok := q.Runner.(Canceller); ok {
				if cerr := c.Cancel(context.WithoutCancel(ctx), taskID); cerr != nil {
					o.Error += " (cancelling its task: " + cerr.Error() + ")"
				}
			}
			q.needsYou(ctx, &write, &o, "The fill ran longer than "+timeout.String()+" and was stopped: open its chat to continue.")
			finish(o)
			return
		}
		if err != nil {
			o.Error = err.Error()
			finish(o)
			return
		}
		if status != "complete" && status != "needs_user" {
			o.Error = "task " + status
			q.needsYou(ctx, &write, &o, "The fill task "+status+": open its chat to continue.")
			finish(o)
			return
		}
		write.Lock()
		err = q.Runner.Finalize(ctx, id)
		write.Unlock()
		if err != nil {
			o.Error = err.Error()
			q.needsYou(ctx, &write, &o, "The fill result could not be recorded: "+err.Error())
			finish(o)
			return
		}
		o.Result = ResultNeedsYou
		if q.Status != nil {
			if st, err := q.Status(ctx, id); err == nil && st == "awaiting_submit" {
				o.Result = ResultParked
			}
		}
		finish(o)
	}

	next := 0
	for {
		if ctx.Err() == nil {
			write.Lock()
			err := q.Slots.Renew(ctx)
			write.Unlock()
			if err != nil {
				q.logf("renewing slots: %v", err)
			}
		}
		for ctx.Err() == nil && next < len(ids) {
			id := ids[next]
			mu.Lock()
			_, dup := running[id]
			mu.Unlock()
			if dup {
				next++
				continue
			}
			write.Lock()
			ok, err := q.Slots.Reserve(ctx, id, max)
			write.Unlock()
			if err != nil {
				wg.Wait()
				return q.report(ids, outcomes), err
			}
			if !ok {
				break
			}
			next++
			mu.Lock()
			running[id] = true
			mu.Unlock()
			wg.Add(1)
			q.logf("starting %s", id)
			go runOne(id)
		}
		mu.Lock()
		idle := len(running) == 0
		mu.Unlock()
		if idle && (next >= len(ids) || ctx.Err() != nil) {
			break
		}
		if ctx.Err() == nil && next < len(ids) {
			write.Lock()
			err := q.Runner.Sweep(ctx)
			write.Unlock()
			if err != nil {
				q.logf("sweep: %v", err)
			}
		}
		select {
		case <-wake:
		case <-time.After(tick):
		case <-ctx.Done():
			// the fills in flight see the same ctx and return.
			wg.Wait()
		}
	}
	wg.Wait()
	return q.report(ids, outcomes), ctx.Err()
}

func (q Queue) report(ids []string, outcomes map[string]Outcome) []Outcome {
	out := make([]Outcome, 0, len(ids))
	for _, id := range ids {
		if o, ok := outcomes[id]; ok {
			out = append(out, o)
		} else {
			out = append(out, Outcome{ApplicationID: id, Result: ResultNotStarted, Error: fmt.Sprintf("not started: %s", "the queue ended first")})
		}
	}
	return out
}
