//go:build doomsday

package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

var errDispatchRefused = coderhost.ErrDispatchRefused

// governedLauncher puts the governor's admission in front of a launcher. It wraps the OUTSIDE of the dispatch gate's launcher
// (HostGate.Install wraps first, this goes on top), so a refusal here costs no ticket, and a start the gate refuses gives the
// slot back. Adopt and Finish are the inner launcher's: they end or find what exists and are never governed.
type governedLauncher struct {
	coderhost.Launcher
	w *GovernorWire
}

type governedBudgetLauncher struct{ governedLauncher }

// GovernLauncher returns l with its turn starts under the governor's admission. A nil wire returns l itself.
func (w *GovernorWire) GovernLauncher(l coderhost.Launcher) coderhost.Launcher {
	if w == nil || l == nil {
		return l
	}
	g := governedLauncher{Launcher: l, w: w}
	if _, ok := l.(coderhost.BudgetLauncher); ok {
		return governedBudgetLauncher{g}
	}
	return g
}

func (g governedLauncher) Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (coderhost.Turn, error) {
	return g.start(profileID, conversationID, turnID, func() (coderhost.Turn, error) {
		return g.Launcher.Start(profileID, conversationID, turnID, message, attachments, missionToken)
	})
}

func (g governedBudgetLauncher) StartCapped(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (coderhost.Turn, error) {
	return g.start(profileID, conversationID, turnID, func() (coderhost.Turn, error) {
		return g.Launcher.(coderhost.BudgetLauncher).StartCapped(profileID, conversationID, turnID, message, attachments, missionToken, capUSD)
	})
}

// start asks the governor for a slot, then makes the turn. With no running run it is mk and nothing else.
func (g governedLauncher) start(profileID, conversationID, turnID string, mk func() (coderhost.Turn, error)) (coderhost.Turn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), govCallTimeout)
	rg, err := g.w.For(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	if rg == nil {
		cancel()
		return mk()
	}
	member := memberID(conversationID, turnID)
	provider := ""
	if g.w.ProviderOf != nil {
		provider = g.w.ProviderOf(profileID, conversationID)
	}
	err = rg.admit(ctx, member, conversationID, provider)
	cancel()
	if err != nil {
		return nil, err
	}
	t, err := mk()
	if err != nil {
		rg.release(member) // a refused or failed start leaks no slot
		return nil, err
	}
	return &governedTurn{Turn: t, rg: rg, member: member, provider: provider}, nil
}

// admit reserves a process slot for the member. Fresh arrivals go straight to a free slot only while nobody waits; otherwise
// they join the queue so that the line stays fair. A member that has to wait is deferred (the host keeps its send pending).
func (rg *runGov) admit(ctx context.Context, member, conv, provider string) error {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.isStarted(member) { // this turn already runs on its slot: a second start never shares it
		return fmt.Errorf("%w: that turn is already running", coderhost.ErrDeferred)
	}
	if rg.claim(member) || rg.holdsSlot(ctx, member) { // reserved for this launch by the queue, even after the claim time
		rg.markStarted(member)
		return nil
	}
	req := governor.Request{Kind: governor.KindProcess, MemberID: member}
	rows, _, derr := rg.q.Depth(ctx, rg.lim.Profile, rg.lim.Generation)
	if derr == nil && rows == 0 {
		rg.setStarting(member, true)
		err := rg.s.TryAdmit(ctx, provider, req)
		switch {
		case err == nil:
			rg.markStarted(member)
			rg.publishLocked(ctx, false)
			return nil
		case errors.Is(err, governor.ErrAlreadyHeld): // taken for this launch meanwhile
			rg.markStarted(member)
			return nil
		case errors.Is(err, governor.ErrNoSlot):
			rg.setStarting(member, false) // no slot was taken: fall through to the queue
		default:
			rg.setStarting(member, false)
			return govRefusal(err)
		}
	} else if derr != nil {
		return govRefusal(derr)
	}
	if !rg.isQueued(member) {
		pl, _ := json.Marshal(govPayload{M: member, P: provider})
		if !rg.queuedInDB(ctx, pl) { // idempotent: a restarted host finds its earlier row
			if _, err := rg.s.Enqueue(ctx, governor.Item{Lead: conv, Class: governor.ClassNormal, Payload: pl}); err != nil {
				if errors.Is(err, governor.ErrQueueFull) {
					return fmt.Errorf("%w: the open-ended run's queue of waiting launches is full (at most %d rows and %d bytes); the send was refused", coderhost.ErrLimit, rg.lim.QueueMaxRows, rg.lim.QueueMaxBytes)
				}
				return govRefusal(err)
			}
			rg.dirty.Store(true)
		}
		rg.setQueued(member)
	}
	if rg.dirty.Load() || time.Since(rg.lastPump) >= govPumpEvery { // waiting sends asked again with nothing changed do not each pump
		rg.pumpLocked(ctx)
	}
	rg.publishLocked(ctx, false)
	if rg.claim(member) {
		return nil
	}
	return fmt.Errorf("%w: waiting for a free slot", coderhost.ErrDeferred)
}

// govRefusal is a governor error as the launch's refusal: a stopped run or a stopped scheduler is the dispatch refusal Stop
// already uses; anything else refuses too (a governor that cannot decide admits nothing).
func govRefusal(err error) error {
	if errors.Is(err, governor.ErrNotRunning) {
		return fmt.Errorf("%w: %v", errDispatchRefused, err)
	}
	return fmt.Errorf("%w: the governor refused the launch: %v", errDispatchRefused, err)
}

// governedTurn gives its slot back when the process ends (or is stopped), exactly once.
type governedTurn struct {
	coderhost.Turn
	rg       *runGov
	member   string
	provider string
	once     sync.Once
}

func (t *governedTurn) done() { t.once.Do(func() { t.rg.release(t.member) }) }

func (t *governedTurn) Wait() error {
	err := t.Turn.Wait()
	t.done()
	if err != nil && t.provider != "" {
		if after, ok := rateLimitFrom(err); ok {
			t.rg.w.RateLimited(t.provider, after)
		}
	}
	return err
}

func (t *governedTurn) Stop() {
	t.Turn.Stop()
	t.done()
}

var (
	rateLimitRE  = regexp.MustCompile(`(?i)\b429\b|rate.?limit`)
	retryAfterRE = regexp.MustCompile(`(?i)retry(?:ing)?[ -]?(?:after|in)[: ]+(\d{1,5})\s*s`)
)

// rateLimitFrom reads a turn's failure for a provider rate limit: best effort, on the error text only (the name of a provider
// and a number of seconds are all that leave this function).
func rateLimitFrom(err error) (time.Duration, bool) {
	m := err.Error()
	if len(m) > 4096 || !rateLimitRE.MatchString(m) {
		return 0, false
	}
	if g := retryAfterRE.FindStringSubmatch(m); g != nil {
		if n, e := strconv.Atoi(g[1]); e == nil {
			return time.Duration(n) * time.Second, true
		}
	}
	return 0, true
}
