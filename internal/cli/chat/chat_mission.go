package chat

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"strings"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/gitsafe"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/tasks/gitevidence"
)

// missionActor is the name the coder lead plans missions under.
const missionActor = "mission-lead"

// missionConfig is the lead's mission setup for this turn: the planning tools
// and prompt need coding access or more and a confirmed mission (the user's
// one click, D2), which a conversation with an open mission session keeps
// across turns and restarts. Below coding access it is nil.
func missionConfig(ctx context.Context, db *sql.DB, profileID, conversationID string, t coderTurn) *dynorg.MissionConfig {
	if !dynorg.MissionAccess(t.access) || conversationID == "" {
		return nil
	}
	store := tasks.NewStore(db)
	store.SetEvidenceChecker(gitevidence.Checker(""))
	m := &dynorg.MissionConfig{
		Store: store, ProfileID: profileID, ConversationID: conversationID,
		Actor: missionActor, Access: t.access, Confirmed: t.missionConfirmed,
	}
	if !m.Confirmed && m.HasOpenSession(ctx) {
		m.Confirmed = true
	}
	return m
}

// missionRun connects the conductor's mission tools (mission_next, mission_assign,
// mission_report, mission_verify, mission_finish, org_spawn's task_id) to the profile's
// task store. It is nil unless the lead is a confirmed mission planner; the session is
// resolved from the conversation by the conductor, never named by the lead.
func missionRun(m *dynorg.MissionConfig, db *sql.DB, turnID string) *dynorg.MissionRun {
	if !m.Planning() {
		return nil
	}
	return &dynorg.MissionRun{Store: m.Store, ProfileID: m.ProfileID, Tokens: agenttokens.NewStore(db),
		Owner: dynorg.TurnOwner(m.ProfileID, m.ConversationID, turnID), Live: turnRunning(db, m.ProfileID),
		Children: newMissionChildren(db, m.ProfileID, m.ConversationID)}
}

// turnRunning reports, for the owner of a turn's mission tokens, whether that turn is still
// active. A turn the database does not know is not running; a database that cannot answer says
// it is, so that a sweep never ends a run it cannot show to be over.
func turnRunning(db *sql.DB, profileID string) func(context.Context, string) bool {
	return func(ctx context.Context, owner string) bool {
		_, turn, ok := strings.Cut(owner, "/")
		if !ok || turn == "" {
			return false
		}
		var status string
		err := db.QueryRowContext(ctx, `SELECT status FROM ai_chat_turns WHERE id = ? AND profile_id = ?`, turn, profileID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		return err != nil || status == "active"
	}
}

// missionMaxAgents is the per-turn worker cap of a turn: a confirmed mission gets the larger
// mission default when the operator never set --org-max-agents; an operator's value, any
// value, stays authoritative (it is bounded 1-20 where it is set).
func missionMaxAgents(settings coderSettings, m *dynorg.MissionConfig) int {
	if m.Planning() && settings.orgMaxAgentsDefaulted && settings.OrgMaxAgents < dynorg.MissionMaxAgents {
		return dynorg.MissionMaxAgents
	}
	return settings.OrgMaxAgents
}

// missionWriters is the writers mode of a turn: a confirmed mission whose operator never set
// --org-writers gets isolated writers (a worktree each, so independent tasks run in parallel)
// when the conversation folder is inside a git work tree with a commit to branch from; an
// operator's value, shared or isolated, stays authoritative, and without a repository the
// default stays shared. Only the operator sets the flag (operatorOrgFlags); an agent cannot.
func missionWriters(ctx context.Context, settings coderSettings, m *dynorg.MissionConfig, cwd string) string {
	if m.Planning() && settings.orgWritersDefaulted && cwdIsGitWorkTree(ctx, cwd) {
		return dynorg.WritersIsolated
	}
	return settings.OrgWriters
}

func cwdIsGitWorkTree(ctx context.Context, cwd string) bool {
	if cwd == "" {
		return false
	}
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	// the folder is the user's (or an agent's): git runs hardened (gitsafe)
	if gitsafe.Command(ctx, "-C", cwd, "rev-parse", "--is-inside-work-tree").Run() != nil {
		return false
	}
	return gitsafe.Command(ctx, "-C", cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}").Run() == nil
}
