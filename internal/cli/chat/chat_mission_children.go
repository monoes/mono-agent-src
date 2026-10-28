package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/taskbind"
)

// The lead's side of mission child sessions (phase P-C of docs/mastermind/plans/2026-10-27-mission-child-sessions.md):
// the dynorg.ChildSpawner over the coder host's store. The caller is built here and nowhere else in the chat
// process: the lead's verified session token (never an argument) plus the binding of this process as the
// database verifies it (coderhost.NewLeadScope). A process that holds a task-bound binding, or whose binding
// does not verify, is not a lead for this purpose and its tasks run in-turn.

// launcherMintsChildTokens says whether the coder host's launcher gives a child session the task token that
// confines it to its one task (phase P-B2, coder_host_childtoken.go). The conductor never spawns a child session
// without it, whatever the mission marks or the policy says: a child without that token would not be bound to its
// task. Two ways it is true, in two kinds of process:
//   - the host's own process registered the launcher that mints (RegisterChildTokenMinter);
//   - any other process (the lead's chat turn) finds a running host that says it mints (registerChildHostProbe):
//     the host, not the lead's process, starts the child's turn and mints its token, so the lead's process asks the
//     host. A host built before this existed does not say so, and a missing or stopped host is a no.
func launcherMintsChildTokens() bool {
	childMinterMu.Lock()
	m, probe := childMinter, childHostProbe
	childMinterMu.Unlock()
	if m != nil && m.CanMintChildTokens() {
		return true
	}
	return probe != nil && probe()
}

// ChildTokenMinter is what the launcher registers (RegisterChildTokenMinter) once it gives each child session a
// task-bound token. It is the only way the host's own process becomes ready.
type ChildTokenMinter interface{ CanMintChildTokens() bool }

var (
	childMinterMu  sync.Mutex
	childMinter    ChildTokenMinter
	childHostProbe func() bool
)

// RegisterChildTokenMinter is called by the host's launcher setup (`coder host run`) when it mints child tokens.
func RegisterChildTokenMinter(m ChildTokenMinter) {
	childMinterMu.Lock()
	childMinter = m
	childMinterMu.Unlock()
	coderhost.SetMissionChildrenReady(launcherMintsChildTokens)
}

// registerChildHostProbe makes a process that is not the host ask the running host (its info file) whether it mints
// child task tokens.
func registerChildHostProbe(cfg *Deps) {
	childMinterMu.Lock()
	childHostProbe = func() bool {
		if !hostRunning(cfg) {
			return false
		}
		info, ok := readHostInfo(cfg)
		return ok && info.ChildTokens
	}
	childMinterMu.Unlock()
	coderhost.SetMissionChildrenReady(launcherMintsChildTokens)
}

// missionChildren is the dynorg.ChildSpawner of one lead conversation.
type missionChildren struct {
	db        *sql.DB
	profileID string
	conv      string
	getenv    func(string) string
	ready     func() bool
}

func newMissionChildren(db *sql.DB, profileID, conversationID string) *missionChildren {
	return &missionChildren{db: db, profileID: profileID, conv: conversationID, getenv: os.Getenv, ready: launcherMintsChildTokens}
}

// Ready is the launcher's capability.
func (m *missionChildren) Ready() bool { return m.ready() }

// lead builds the lead's scope and says why this process cannot act as the lead ("" = it can).
func (m *missionChildren) lead(ctx context.Context) (*coderhost.Store, coderhost.LeadScope, string) {
	store := coderhost.NewStore(m.db)
	c, err := store.CallerFor(m.getenv(coderhost.SessionTokenEnv), m.profileID)
	if err != nil || c.ConversationID != m.conv {
		return store, coderhost.LeadScope{}, "this chat is not a coder session turn"
	}
	if taskbind.Resolve(ctx, m.db, m.profileID, m.getenv(agenttokens.EnvMissionToken)).State != taskbind.None {
		return store, coderhost.LeadScope{}, "this process is a worker of a mission"
	}
	return store, coderhost.NewLeadScope(c, coderhost.BindingNone), ""
}

// Facts is what the conductor reads about one task. A lead that cannot have children reads Blocked.
func (m *missionChildren) Facts(ctx context.Context, sid, tid int64) (dynorg.ChildFacts, error) {
	store, scope, why := m.lead(ctx)
	pol, err := store.GetPolicy(m.profileID, sid)
	if err != nil {
		return dynorg.ChildFacts{}, err
	}
	f := dynorg.ChildFacts{Mode: pol.Mode}
	if row, have, err := store.Child(m.profileID, sid, tid); err != nil {
		return f, err
	} else if have {
		f.Row, f.EndReason, f.Child = row.State, row.EndReason, row.ChildConversationID
		f.Exhausted = row.Attempts >= coderhost.MaxChildAttempts
	}
	if on, err := store.MissionChildrenEnabled(); err != nil {
		return f, err
	} else if !on {
		f.Blocked = "child sessions are off (operator setting)" // the mark and its row stay as they are
		return f, nil
	}
	if why != "" {
		f.Blocked = why
		return f, nil
	}
	lf, err := store.LeadFacts(scope, m.profileID, sid, tid)
	if err != nil {
		f.Blocked = "this session does not lead that mission"
		return f, nil
	}
	f.RoomOK, f.RoomReason, f.Exhausted = lf.Room.OK, lf.Room.Reason, lf.Exhausted
	if !lf.MayHaveChildren {
		f.Blocked = "this session is a child or not a master, so it cannot have child sessions"
	}
	return f, nil
}

// Spawn reserves the task's row and starts its session. In mode all a task whose earlier session ended is marked
// again first (the lead's own mark, within the same policy); the access, folder and budget of the session are
// the host's, never taken from here.
func (m *missionChildren) Spawn(ctx context.Context, sid, tid int64, brief string) error {
	if !m.Ready() {
		return errors.New("the launcher cannot give a child session its task token yet")
	}
	store, scope, why := m.lead(ctx)
	if why != "" {
		return errors.New(why)
	}
	lf, err := store.LeadFacts(scope, m.profileID, sid, tid)
	if err != nil {
		return err
	}
	switch lf.Row {
	case coderhost.ChildEnded, coderhost.ChildFailed, coderhost.ChildGone, coderhost.ChildReleased:
		if lf.Mode != coderhost.ModeAll || lf.Exhausted || !dynorg.ChildAutoRetry(lf.EndReason) {
			return fmt.Errorf("the task's session ended: it is marked again by the lead")
		}
		row, _, _ := store.Child(m.profileID, sid, tid)
		if _, err := store.Mark(scope, m.profileID, sid, tid, coderhost.MarkOptions{Size: row.Size, BudgetUSD: row.BudgetUSD}); err != nil {
			if errors.Is(err, coderhost.ErrChildUnsettled) {
				return fmt.Errorf("%w: %v", dynorg.ErrChildUnsettled, err)
			}
			return err
		}
	}
	if _, err := store.SpawnForTask(scope, m.profileID, sid, tid, brief); err != nil {
		if errors.Is(err, coderhost.ErrChildrenFull) {
			return fmt.Errorf("%w: %v", dynorg.ErrChildrenFull, err)
		}
		return err
	}
	return nil
}

// Mark records the lead's wish at plan time.
func (m *missionChildren) Mark(ctx context.Context, sid, tid int64, size string, budgetUSD float64) error {
	store, scope, why := m.lead(ctx)
	if why != "" {
		return errors.New(why)
	}
	_, err := store.Mark(scope, m.profileID, sid, tid, coderhost.MarkOptions{Size: size, BudgetUSD: budgetUSD})
	return err
}

// maxChildReport bounds what a child's last words are taken as (a worker's report is bounded the same way).
const maxChildReport = 12000

// Report is the last stretch of assistant prose of the child's newest turn, from the journal. It is data: the
// conductor attaches it to the task and fences it wherever it is shown.
func (m *missionChildren) Report(_ context.Context, childConversation string) string {
	if childConversation == "" {
		return ""
	}
	store, err := ai.NewAIStore(m.db)
	if err != nil {
		return ""
	}
	turns, _, err := store.ListTurns(childConversation, m.profileID, "", 1)
	if err != nil || len(turns) == 0 {
		return ""
	}
	var parts []string
	var last string
	var after int64
	for page := 0; page < 20; page++ {
		evs, err := store.GetEvents(childConversation, turns[0].ID, m.profileID, after, 1000)
		if err != nil || len(evs) == 0 {
			break
		}
		for _, ev := range evs {
			after = ev.Seq
			if ev.Type != chatevents.EventAssistantDelta {
				continue
			}
			var p chatevents.AssistantDeltaPayload
			if json.Unmarshal(ev.Payload, &p) != nil || p.AgentID != "" {
				continue
			}
			if p.PartID != last || len(parts) == 0 {
				parts, last = []string{""}, p.PartID // the last part of prose is the report
			}
			parts[0] += p.Text
		}
		if len(evs) < 1000 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.TrimSpace(parts[0])
	if len(out) > maxChildReport {
		out = out[len(out)-maxChildReport:]
	}
	return out
}
