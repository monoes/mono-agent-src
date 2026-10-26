package dynorg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

// tokenMargin is how long a mission token outlives the run's own timeout: the run is cut at
// the timeout, and the token is revoked when the run ends, so the expiry is only the backstop.
const tokenMargin = 5 * time.Minute

// runOwner names this run for the tokens it mints, so that they can be told from another run's.
func (m *MissionRun) runOwner() string {
	m.ownerOnce.Do(func() {
		var b [8]byte
		_, _ = rand.Read(b[:])
		m.owner = "run-" + hex.EncodeToString(b[:])
	})
	return m.owner
}

// bindMission hands a mission worker or verifier the token that binds its process to its
// session, task, role and name, in the environment of its run (agenttokens.EnvMissionToken).
// The returned function revokes it, and the caller defers it: the token ends with the run.
// The MONOAGENT_TASK_* labels stay in the environment for the brief and the operator guard;
// they bind nothing.
//
// A conductor without a token store (a test's fake) binds nothing: the process is an
// ordinary agent, which cannot record a verdict in a mission session.
func (c *Conductor) bindMission(ctx context.Context, w *worker, env map[string]string) (func(), error) {
	if w.task == nil || c.cfg.Missions == nil || c.cfg.Missions.Tokens == nil {
		return func() {}, nil
	}
	mc := c.cfg.Missions
	role, ttl := agenttokens.RoleWorker, mc.timeout()
	if w.task.verifier {
		role, ttl = agenttokens.RoleVerifier, mc.verifierTimeout()
	}
	tok, secret, err := mc.Tokens.CreateMission(ctx, mc.ProfileID, agenttokens.MissionBinding{
		Session: w.task.session, Task: w.task.task.ID, Role: role, Actor: w.task.name, Owner: mc.runOwner(),
	}, ttl+tokenMargin)
	if err != nil {
		return nil, fmt.Errorf("task #%d: its process could not be bound to the task: %w", w.task.task.ID, err)
	}
	env[agenttokens.EnvMissionToken] = secret
	return func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
		defer cancel()
		_, _ = mc.Tokens.Revoke(rctx, mc.ProfileID, tok.ID)
	}, nil
}
