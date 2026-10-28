package dynorg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/logging"
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
	_, unbind, err := c.mintMission(ctx, w, env)
	return unbind, err
}

// ancestryRuntimes are the runtimes whose shells and tool servers are all descendants of the
// process monomind starts, so a mission token can be bound to that process tree. A runtime that
// is not listed is left unbound (today's behaviour: the token is accepted from any process that
// holds it): a runtime that runs its tools in a daemon outside the tree would otherwise lose its
// worker. Only claude is listed; the others are unverified live.
var ancestryRuntimes = map[string]bool{"claude": true}

// bindMissionProcess is what binds the token id to the run's process once it has started: the
// pid monomind reports and its start stamp. Where the runtime is not listed, ancestry cannot be
// read (not Linux) or the stamp cannot be read, the token stays unbound; a failure is journalled
// and also leaves it unbound (it is then accepted as before).
func (c *Conductor) bindMissionProcess(m Model, id string) func(pid int) {
	mc := c.cfg.Missions
	if id == "" || mc == nil || mc.Tokens == nil || !ancestryRuntimes[m.Runtime] || !agenttokens.MissionProcessSupported() {
		return nil
	}
	return func(pid int) {
		stamp := ""
		for i := 0; i < 3 && stamp == ""; i++ {
			if stamp = agenttokens.ProcessStamp(pid); stamp == "" {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if stamp == "" {
			c.missionBindFailed("the worker's start time could not be read")
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 2*time.Second)
		defer cancel()
		if err := mc.Tokens.BindMissionProcess(ctx, mc.ProfileID, id, pid, stamp); err != nil {
			c.missionBindFailed(err.Error())
		}
	}
}

// missionBindFailed journals and logs a token that stays unbound: it then works from any process
// that holds it, until the run ends.
func (c *Conductor) missionBindFailed(why string) {
	logging.FromContext(c.ctx).Warn("mission token left unbound to its worker's process", "why", why)
	c.notice("mission_token_process_unbound",
		"A worker's task token could not be bound to its process ("+why+"); it works from any process until it ends.", chatevents.SeverityWarning)
}

// mintMission is bindMission that also returns the id of the token it minted ("" when none).
func (c *Conductor) mintMission(ctx context.Context, w *worker, env map[string]string) (string, func(), error) {
	if w.task == nil || c.cfg.Missions == nil || c.cfg.Missions.Tokens == nil {
		return "", func() {}, nil
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
		return "", nil, fmt.Errorf("task #%d: its process could not be bound to the task: %w", w.task.task.ID, err)
	}
	env[agenttokens.EnvMissionToken] = secret
	return tok.ID, func() { c.revokeMissionToken(mc, tok.ID) }, nil
}

// revokeMissionToken ends a token with its run. A token that is already gone is done. A failed revoke is retried, then journalled:
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
		// A token the sweep purged (it had expired) is already gone: nothing is left to revoke.
		if err == nil || errors.Is(err, agenttokens.ErrNotFound) {
			return
		}
	}
	c.notice("mission_token_revoke_failed",
		"A worker's task token could not be revoked ("+err.Error()+"); it ends when it expires.", chatevents.SeverityWarning)
}
