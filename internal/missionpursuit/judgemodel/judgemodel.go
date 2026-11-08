// Package judgemodel is judge.Model over the product's one AI path, the monomind runner (`monomind agent exec`), for the
// judge of a mission's pursuit. It is the shape of the doomsday strategy judge's adapter (one turn, no tools, a throw-away
// working directory, the reply bounded while it streams), plus the cost of the turn: after the turn it tells the host what
// the runtime measured through judgecost, even for a turn that failed or was cancelled.
//
// The prompt is whatever judge.Request carries; the pursuit host puts only facts it measured there (counts, enums, ids and
// the goal criteria the person confirmed). This package never adds text and never reads a file.
//
// It carries no key and stores nothing: the runtime is the AI account already logged in on this computer. The Pin names
// the exact model, so a model that equals the lead's or the verifier's can be refused (Refuse).
package judgemodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
	"github.com/monoes/mono-agent/internal/monomind"
)

// ExecFunc is monomind.Exec; tests give a fake.
type ExecFunc func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)

// Config builds the model. Exec and Workspace default to the product's.
type Config struct {
	Pin       string // "runtime/model" or "runtime/model@version": required
	Exec      ExecFunc
	Workspace func() (string, error)
	Timeout   time.Duration // one call; 0 = three minutes
}

const defaultTimeout = 3 * time.Minute

type model struct {
	runtime, model, pin string
	exec                ExecFunc
	workspace           func() (string, error)
	timeout             time.Duration
}

// New builds the judge model for a pin. A pin that does not name a runtime and a model is refused.
func New(c Config) (judge.Model, error) {
	rt, md, ok := strings.Cut(c.Pin, "/")
	name, _, _ := strings.Cut(md, "@")
	if !ok || rt == "" || name == "" || strings.ContainsAny(c.Pin, " \t\r\n") {
		return nil, fmt.Errorf("judgemodel: the pin %q is not runtime/model[@version]", c.Pin)
	}
	m := model{runtime: rt, model: name, pin: c.Pin, exec: c.Exec, workspace: c.Workspace, timeout: c.Timeout}
	if m.exec == nil {
		m.exec = monomind.Exec
	}
	if m.workspace == nil {
		m.workspace = func() (string, error) { return monomind.SandboxWorkspaceDir("pursuit-judge") }
	}
	if m.timeout <= 0 {
		m.timeout = defaultTimeout
	}
	return m, nil
}

// Refuse is the independence rule: a pin may not judge when it is empty or equals one of the forbidden pins (the lead's and
// the finish verifier's). At least one forbidden pin is required, so that the rule is never vacuous.
func Refuse(forbidden ...string) (func(pin string) bool, error) {
	var set []string
	for _, f := range forbidden {
		if f != "" {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		return nil, errors.New("judgemodel: the independence rule needs at least one pin the judge may not equal (the lead's or the verifier's)")
	}
	return func(pin string) bool {
		if pin == "" {
			return true
		}
		for _, f := range set {
			if pin == f {
				return true
			}
		}
		return false
	}, nil
}

func (m model) Pin() string { return m.pin }

func (m model) Complete(ctx context.Context, req judge.Request) ([]byte, error) {
	cwd, err := m.workspace()
	if err != nil {
		judgecost.Report(ctx, 0, true) // no turn was started: nothing was spent
		return nil, err
	}
	// The reply is bounded while it streams: past the cap the turn is cancelled, so an oversized answer is never held whole.
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	var seen int
	over := false
	res, err := m.exec(ctx2, monomind.ExecOptions{
		Runtime: m.runtime, Model: m.model, Prompt: req.User, SystemPrompt: req.System,
		Cwd: cwd, Timeout: m.timeout, BudgetUSD: judgecost.PerCallCapUSD, MaxTurns: 1, Sandbox: monomind.TurnSandboxMode,
	}, func(ev monomind.Event) {
		if seen += len(ev.Text); seen > types.MaxJudgmentBytes && !over {
			over = true
			cancel()
		}
	})
	if res != nil && res.HasCostUSD {
		judgecost.Report(ctx, res.CostUSD, true)
	} else {
		judgecost.Report(ctx, 0, false) // a turn ran (or tried to) and the runtime said nothing: the host charges the cap
	}
	if over {
		return nil, judge.ErrReplyTooLong
	}
	if err != nil {
		return nil, err
	}
	if res == nil || res.Err != nil || !res.SawDone || res.ExitCode != 0 {
		return nil, errors.New("the judging turn did not complete")
	}
	if len(res.ResultText) > types.MaxJudgmentBytes {
		return nil, judge.ErrReplyTooLong
	}
	return judge.ReadReply(strings.NewReader(res.ResultText))
}
