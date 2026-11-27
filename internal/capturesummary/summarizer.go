package capturesummary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Answer is what a runtime turn produced.
type Answer struct {
	Text    string
	CostUSD float64
}

// Target is the runtime, and optionally the model, that writes a summary.
type Target struct {
	Runtime string
	// Model is passed as --model; empty leaves it to the runtime.
	Model string
}

// String names the target the way summary.md's footer does:
// "claude" or "claude (claude-haiku-4-5-20251001)".
func (t Target) String() string {
	if t.Model == "" {
		return t.Runtime
	}
	return fmt.Sprintf("%s (%s)", t.Runtime, t.Model)
}

// RunFunc asks t for a completion of prompt. The production one is
// ExecRunner (monomind agent exec); tests substitute a stub.
type RunFunc func(ctx context.Context, t Target, prompt string) (Answer, error)

// DefaultRuntime is the agent runtime summaries use unless configured.
const DefaultRuntime = "claude"

// IsDisabled reports a runtime value that turns summaries off: a bridge
// configured with one of these records every requested summary as an
// error saying so, rather than ignoring it.
func IsDisabled(runtime string) bool {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "off", "none", "disabled", "0", "false":
		return true
	}
	return false
}

// DefaultTimeout bounds one summary turn.
const DefaultTimeout = 5 * time.Minute

// queueDepth is how many summaries may wait behind the one running. A
// person saving pages by hand never gets near it; a batch capture of fifty
// tabs with "summarize" on would, and the overflow is recorded as an error
// on each capture rather than piling up agent processes.
const queueDepth = 16

type job struct {
	dir  string
	meta capture.Meta
	kind string
}

// Summarizer runs capture summaries one at a time in the background.
type Summarizer struct {
	Runtime string
	Run     RunFunc
	// Catalog checks a runtime a capture names itself (meta.summarize
	// .runtime) against what is installed. Nil means only the default
	// runtime may be used.
	Catalog       *Catalog
	MaxInputChars int
	Timeout       time.Duration
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Logf, when set, receives one line per summary outcome.
	Logf func(format string, args ...any)
	// OnDone, when set, is called after each summary finishes: the bridge
	// queues the capture for indexing again, so summary.md is searchable.
	OnDone func(dir string, st Status)

	once   sync.Once
	queue  chan job
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a summarizer for runtime ("" means DefaultRuntime).
func New(runtime string, run RunFunc) *Summarizer {
	if strings.TrimSpace(runtime) == "" {
		runtime = DefaultRuntime
	}
	return &Summarizer{Runtime: strings.TrimSpace(runtime), Run: run}
}

func (s *Summarizer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Summarizer) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Summarizer) start() {
	s.once.Do(func() {
		s.queue = make(chan job, queueDepth)
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.wg.Add(1)
		go s.worker()
	})
}

// Close stops the worker, cancelling a summary in flight. Summaries still
// queued keep their "pending" status; a reader treats them as stalled
// after StaleAfter.
func (s *Summarizer) Close() {
	if s == nil {
		return
	}
	s.start() // so there is always something to cancel
	s.cancel()
	s.wg.Wait()
}

// Handle is the extension server's after-write hook: a capture that asks
// for a summary gets one queued; every other capture is left alone.
func (s *Summarizer) Handle(res *capture.Result) {
	if s == nil || res == nil {
		return
	}
	kind, ok := Requested(res.Meta)
	if !ok {
		return
	}
	if err := s.Enqueue(res.Path, res.Meta, kind); err != nil {
		s.logf("summary not queued for %s: %v", res.Path, err)
	}
}

// Enqueue records the summary as pending and queues it. It never blocks:
// with the queue full, or summaries turned off, the reason is written to
// summary.json and returned.
func (s *Summarizer) Enqueue(dir string, meta capture.Meta, kind string) error {
	req, _ := RequestOf(meta)
	st := Status{Status: StatePending, Kind: kind, Runtime: s.Runtime, Model: req.Model, RequestedAt: stamp(s.now())}
	if req.Runtime != "" {
		st.Runtime = req.Runtime
	}
	if why := refusal(meta); why != "" {
		st.Status, st.Runtime, st.Model, st.Error, st.FinishedAt = StateError, "", "", why, st.RequestedAt
		return errors.Join(errors.New(why), writeStatus(dir, st))
	}
	if IsDisabled(s.Runtime) {
		st.Status, st.Runtime, st.Model = StateError, "", ""
		st.Error = "summaries are turned off on this bridge (MONOAGENT_SUMMARY_RUNTIME / --summary-runtime)"
		st.FinishedAt = st.RequestedAt
		return errors.Join(errors.New(st.Error), writeStatus(dir, st))
	}
	if s.Run == nil {
		st.Status, st.Error, st.FinishedAt = StateError, "no summary runner is configured", st.RequestedAt
		return errors.Join(errors.New(st.Error), writeStatus(dir, st))
	}
	if err := writeStatus(dir, st); err != nil {
		return err
	}
	s.start()
	select {
	case s.queue <- job{dir: dir, meta: meta, kind: kind}:
		return nil
	default:
		st.Status, st.FinishedAt = StateError, stamp(s.now())
		st.Error = fmt.Sprintf("too many summaries waiting (%d); this one was skipped", queueDepth)
		return errors.Join(errors.New(st.Error), writeStatus(dir, st))
	}
}

func (s *Summarizer) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.queue:
			st := s.Summarize(s.ctx, j.dir, j.meta, j.kind)
			if st.Status == StateDone {
				s.logf("summary written for %s (%s, $%.2f)", j.dir, Target{st.Runtime, st.Model}, st.CostUSD)
			} else {
				s.logf("summary failed for %s: %s", j.dir, st.Error)
			}
			if s.OnDone != nil {
				s.OnDone(j.dir, st)
			}
		}
	}
}

// Summarize writes one capture's summary now, synchronously, and returns
// the status it recorded. The capture is never modified beyond summary.md
// and summary.json.
func (s *Summarizer) Summarize(ctx context.Context, dir string, meta capture.Meta, kind string) Status {
	req, _ := RequestOf(meta)
	st := Status{Status: StateRunning, Kind: kind, Runtime: s.Runtime, Model: req.Model, StartedAt: stamp(s.now())}
	if req.Runtime != "" {
		st.Runtime = req.Runtime
	}
	if prev, err := ReadStatus(dir); err == nil {
		st.RequestedAt = prev.RequestedAt
	}
	fail := func(err error) Status {
		st.Status, st.Error, st.FinishedAt = StateError, err.Error(), stamp(s.now())
		if werr := writeStatus(dir, st); werr != nil {
			st.Error += "; and recording that failed: " + werr.Error()
		}
		return st
	}

	if why := refusal(meta); why != "" {
		return fail(errors.New(why))
	}
	target, err := s.resolve(ctx, req)
	if err != nil {
		return fail(err)
	}
	st.Runtime, st.Model = target.Runtime, target.Model

	in, err := LoadInput(dir, meta, kind, s.MaxInputChars)
	if err != nil {
		return fail(err)
	}
	st.Source, st.InputChars, st.Truncated = in.Source, len([]rune(in.Content)), in.Truncated
	if err := writeStatus(dir, st); err != nil {
		return fail(err)
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	watched, sandbox := monomind.WatchSandbox(turnCtx)
	answer, err := s.Run(watched, target, Prompt(in))
	st.CostUSD, st.Sandbox = answer.CostUSD, sandbox.Missing()
	if err != nil {
		if errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
			return fail(fmt.Errorf("%s did not answer within %s", target, timeout))
		}
		return fail(fmt.Errorf("%s: %w", target, err))
	}
	if strings.TrimSpace(answer.Text) == "" {
		return fail(fmt.Errorf("%s returned an empty summary", target))
	}
	doc := Document(in, target.String(), answer.Text, s.now().UTC().Format("2006-01-02"))
	if err := writeAtomic(dir, SummaryFile, []byte(doc)); err != nil {
		return fail(fmt.Errorf("write %s: %w", SummaryFile, err))
	}
	st.Status, st.FinishedAt = StateDone, stamp(s.now())
	if err := writeStatus(dir, st); err != nil {
		return fail(err)
	}
	return st
}

// resolve settles which runtime and model write this summary: the
// capture's own choice when it made one and it checks out, else the
// bridge's default runtime with no model override. A choice that does not
// check out fails the summary with the reason, rather than quietly using a
// different AI from the one picked.
func (s *Summarizer) resolve(ctx context.Context, req Request) (Target, error) {
	t := Target{Runtime: s.Runtime, Model: strings.TrimSpace(req.Model)}
	if t.Model != "" && !ValidModel(t.Model) {
		return t, fmt.Errorf("%q is not a model id", t.Model)
	}
	id := strings.TrimSpace(req.Runtime)
	if id == "" || id == s.Runtime {
		return t, nil
	}
	if !ValidRuntimeID(id) {
		return t, fmt.Errorf("%q is not an agent runtime id", id)
	}
	if s.Catalog == nil {
		return t, fmt.Errorf("this bridge only writes summaries with %s, not %s", s.Runtime, id)
	}
	rt, err := s.Catalog.Lookup(ctx, id)
	if errors.Is(err, ErrUnknownRuntime) {
		return t, fmt.Errorf("%s is not installed on this machine (agent scan)", id)
	}
	if err != nil {
		return t, fmt.Errorf("cannot check that %s is installed: %w", id, err)
	}
	t.Runtime = rt.ID
	return t, nil
}
