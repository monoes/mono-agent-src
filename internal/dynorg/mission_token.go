package dynorg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// tokenMargin is how long a mission token outlives the run's own timeout: the run is cut at
// the timeout, and the token is revoked when the run ends, so the expiry is only the backstop.
const tokenMargin = 5 * time.Minute

// runOwner names the owner of the tokens this run mints. A chat sets Owner (stable for its
// conversation), so a later conductor of the same conversation can name what an earlier one
// left; without one (a test's fake) it is random per run, and nothing is ever swept by it.
func (m *MissionRun) runOwner() string {
	if m.Owner != "" {
		return m.Owner
	}
	m.ownerOnce.Do(func() {
		var b [8]byte
		_, _ = rand.Read(b[:])
		m.owner = "run-" + hex.EncodeToString(b[:])
	})
	return m.owner
}

// ConversationOwner names the runs of a conversation of a profile: the group a sweep covers.
func ConversationOwner(profileID, conversationID string) string {
	h := sha256.Sum256([]byte(profileID + "\x00" + conversationID))
	return "conv-" + hex.EncodeToString(h[:8])
}

// TurnOwner is the owner of the mission tokens one turn of a conversation mints: stable for the
// turn, so a conductor that resumes it names the same owner, and different from the owner of any
// other turn, so a sweep by one run can tell another run's tokens from its own.
func TurnOwner(profileID, conversationID, turnID string) string {
	return ConversationOwner(profileID, conversationID) + "/" + turnID
}

// sweepMissionTokens revokes the tokens an earlier run of this conversation left behind (it
// crashed, or was killed, so nothing revoked them), and purges the expired ones. It runs once per
// Conductor, before this one mints its first token. It takes only the tokens of a run that
// MissionRun.Live does not report as running: a second conductor of the conversation (a second
// turn the chat admitted) must not end the first one's workers. With no Live check, every other
// run's tokens count as stale. A failure is journalled; the stale tokens then end at their expiry.
func (c *Conductor) sweepMissionTokens(mc *MissionRun) {
	if mc == nil || mc.Tokens == nil {
		return
	}
	c.msched.sweepOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
		defer cancel()
		group, _, _ := strings.Cut(mc.runOwner(), "/")
		if _, err := mc.Tokens.SweepMissions(ctx, group, mc.runOwner(), mc.Live); err != nil {
			c.notice("mission_token_sweep_failed",
				"Tokens of an earlier run of this mission could not be revoked ("+err.Error()+"); they end when they expire.", chatevents.SeverityWarning)
		}
	})
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
	c.sweepMissionTokens(mc)
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
	return func() { c.revokeMissionToken(mc, tok.ID) }, nil
}

// revokeMissionToken ends a token with its run. A failed revoke is retried, then journalled:
// the token then lives until it expires or the next conductor of the conversation sweeps it.
func (c *Conductor) revokeMissionToken(mc *MissionRun, id string) {
	revoke := mc.revokeFn
	if revoke == nil {
		revoke = func(ctx context.Context, id string) error {
			_, err := mc.Tokens.Revoke(ctx, mc.ProfileID, id)
			return err
		}
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
		err = revoke(rctx, id)
		cancel()
		if err == nil {
			return
		}
	}
	c.notice("mission_token_revoke_failed",
		"A worker's task token could not be revoked ("+err.Error()+"); it ends when it expires.", chatevents.SeverityWarning)
}
