package coderhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// session_spawn for a lead (docs/mastermind/plans/2026-10-26-coder-session-host.md, 3.2, and
// 2026-10-28-mission-research-directions.md, section 3): the lead of a mission or of a direction creates a child coder
// session, a whole conversation, together with a named task session of its own below the lead's task session. Both are
// made in one flow: the conversation first (every limit of a master's child applies: depth, max_children, the spawn
// rate, the live cap of the tree, access never above the parent's, the carve of the budget), then the task session,
// and a failure of the second removes the first. The advisory (spawnadvice.go) refuses a small piece of work once.
//
// Who may call: the lead scope of a verified session token with no task binding, as for the mission child tools, so a
// worker, a verifier and any helper the conductor started (its token is blanked) are refused. The spawner's task
// session must be running: a draft plan the operator has not approved spawns nothing.

// SessionKinds a lead may spawn. The mission itself and a plain board are not spawned.
var SpawnKinds = []string{tasks.SessionKindDirection, tasks.SessionKindResearch, tasks.SessionKindTask}

// A session made by a lead is never "giant" by default (the approval rules send giant work to a person): at most
// MaxSpawnedSessionTasks tasks, and a budget of at most SpawnShareMaxPct percent of what the lead's session has left,
// DefaultSpawnSharePct of it when the lead names none.
const (
	MaxSpawnedSessionTasks = 25
	SpawnShareMaxPct       = 40
	DefaultSpawnSharePct   = 20
)

// childShare is the budget of the new session, for its task session and its carve: what the lead asked for, else the
// default share of what its own session has left. What is left is the session's Commitment (spend, worker
// reservations and what its children hold), not only budget - spent. A lead whose session has no budget gets what it
// asked for (0: none).
func childShare(com tasks.Commitment, asked float64) (float64, error) {
	left, bounded := com.Left()
	if !bounded {
		return asked, nil
	}
	if left <= 0 {
		return 0, fmt.Errorf("%w: your session has no budget left to share", ErrLimit)
	}
	if asked <= 0 {
		return left * DefaultSpawnSharePct / 100, nil
	}
	if asked > left*SpawnShareMaxPct/100 {
		return 0, fmt.Errorf("%w: a new session takes at most %d%% of what yours has left ($%.2f of $%.2f); ask for less", ErrLimit, SpawnShareMaxPct, left*SpawnShareMaxPct/100, left)
	}
	return asked, nil
}

// MaxScopeTasks bounds the task ids one request names.
const MaxScopeTasks = 50

// SessionSpawnRequest is what the lead asks for.
type SessionSpawnRequest struct {
	Title     string
	Goal      string // the new session's goal, also its first message
	Kind      string // direction, research or task
	Reason    string // one of SpawnReasons
	TaskIDs   []int64
	BudgetUSD float64 // carved out of the lead's remaining budget, like session_spawn's
	Access    string  // at most the lead's, never full
	Runtime   string
	Model     string
}

// SessionSpawned is the new pair.
type SessionSpawned struct {
	Spawned
	TaskSessionID int64  `json:"taskSessionId"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Depth         int    `json:"depth"`
}

// leadActor is the task-store identity of a lead that starts a session below its own: bound to its task session, with
// its chat named so the new session may link to it. The name is made from the chat id, so one lead cannot rename or
// stop what another started.
func leadActor(conversationID string, session int64, child string) tasks.Actor {
	name := "lead-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, conversationID)
	return tasks.Actor{Kind: tasks.Agent, Name: name[:min(len(name), 40)], Session: session, Conversation: child}
}

// scopeHash identifies a request for the "refused once" memory: the same title, kind and tasks.
func scopeHash(kind, title string, ids []int64) string {
	sorted := slices.Clone(ids)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%v", kind, strings.ToLower(strings.Join(strings.Fields(title), " ")), sorted)))
	return hex.EncodeToString(h[:])
}

// LeadsOpenSession says whether the conversation leads an open task session (draft, running or paused).
func (s *Store) LeadsOpenSession(profileID, conversationID string) (bool, error) {
	open, err := s.openMissionSession(profileID, conversationID)
	return open != 0, err
}

// SpawnSession creates a child coder session of the caller with a task session of its own below the caller's. sc is
// the lead's scope (NewLeadScope), profile the call's profile.
func (s *Store) SpawnSession(ctx context.Context, sc CallerScope, profile string, req SessionSpawnRequest) (SessionSpawned, error) {
	sid, err := s.LeadMission(sc, profile)
	if err != nil {
		return SessionSpawned{}, err
	}
	c := sc.(LeadScope).Caller
	title := strings.Join(strings.Fields(req.Title), " ")
	switch {
	case title == "" || len([]rune(title)) > tasks.MaxTitleRunes:
		return SessionSpawned{}, fmt.Errorf("%w: a session needs a title of at most %d characters", ErrInvalid, tasks.MaxTitleRunes)
	case !slices.Contains(SpawnKinds, req.Kind):
		return SessionSpawned{}, fmt.Errorf("%w: kind is %s", ErrInvalid, strings.Join(SpawnKinds, ", "))
	case len(req.TaskIDs) > MaxScopeTasks:
		return SessionSpawned{}, fmt.Errorf("%w: at most %d tasks", ErrInvalid, MaxScopeTasks)
	case len(req.Goal) > MaxMessageBytes:
		return SessionSpawned{}, fmt.Errorf("%w: the goal is over %d bytes", ErrInvalid, MaxMessageBytes)
	case req.BudgetUSD < 0 || math.IsNaN(req.BudgetUSD) || math.IsInf(req.BudgetUSD, 0):
		return SessionSpawned{}, fmt.Errorf("%w: the budget must be a number of at least 0", ErrInvalid)
	}
	ts := tasks.NewStore(s.db)
	actor := leadActor(c.ConversationID, sid, "")
	se, err := ts.GetSession(ctx, profile, sid, actor)
	if err != nil {
		return SessionSpawned{}, errMissionRefused
	}
	if se.State != tasks.SessionRunning || se.Archived {
		return SessionSpawned{}, fmt.Errorf("%w: your session #%d is %s: sessions are spawned only from a running one (the operator approves the plan first)", ErrInvalid, sid, se.State)
	}
	com, err := ts.Commitment(ctx, profile, sid)
	if err != nil {
		return SessionSpawned{}, err
	}
	budget, err := childShare(com, req.BudgetUSD)
	if err != nil {
		return SessionSpawned{}, err
	}
	// A limit is checked before the advisory, so a refusal that cannot be repeated away does not use up the advisory's one.
	if _, err := s.spawnRoom(c); err != nil {
		s.recordSpawn(c, title, req, SpawnFacts{}, AdviceRefuse, err.Error())
		return SessionSpawned{}, err
	}
	facts, err := s.scopeFacts(ctx, ts, profile, sid, actor, req.TaskIDs)
	if err != nil {
		return SessionSpawned{}, err
	}
	hash := scopeHash(req.Kind, title, req.TaskIDs)
	refused, err := s.advisedBefore(profile, c.ConversationID, hash)
	if err != nil {
		return SessionSpawned{}, err
	}
	adv, err := AdviseSpawn(facts, req.Reason, refused)
	if err != nil {
		return SessionSpawned{}, err
	}
	if adv.Decision == AdviceRefuse {
		if err := s.markAdvised(profile, c.ConversationID, hash); err != nil {
			return SessionSpawned{}, err
		}
		s.recordSpawn(c, title, req, facts, AdviceRefuse, adv.Why)
		return SessionSpawned{}, ErrSpawnAdvice
	}
	// The spawn is not one operation: the conversation, its link and carve, the task session and then the acceptance row
	// are separate writes. An INTENT row (spawn_intent.go, one per lead) is written first and ends, in one transaction with the
	// acceptance row, only when everything is in place; a crash in between leaves the intent for the host's sweep, and
	// until then any further spawn of the same lead is refused. The acceptance is a master row nothing rotates out, so it is
	// written only once every check of the spawn (access, runtime, model, folder, the limits, the carve, the task
	// session) has passed, in the last step before the first message. A failed write undoes the spawn.
	intent, err := s.openSpawnIntent(profile, c.ConversationID, hash)
	if err != nil {
		return SessionSpawned{}, err
	}
	var made tasks.Session
	out, err := s.spawnWith(c, SpawnRequest{Scoped: req.Kind == tasks.SessionKindResearch, BudgetUSD: budget, Access: req.Access, Runtime: req.Runtime, Model: req.Model}, spawnHooks{
		keep:      draftOnlyGuard(profile), // the undo never deletes a conversation whose task session the operator approved
		onCreated: func(conv string) error { return s.setIntentChild(intent, conv) },
		beforeMessage: func(conv string) error {
			var err error
			made, err = ts.CreateSession(ctx, profile, tasks.SessionInput{Title: title, Goal: req.Goal, ConversationID: conv, ParentSessionID: sid, Kind: req.Kind,
				MaxTasks: min(MaxSpawnedSessionTasks, se.MaxTasks)},
				leadActor(c.ConversationID, sid, conv))
			if err != nil {
				return err
			}
			if s.afterSpawnSessionCreated != nil {
				s.afterSpawnSessionCreated()
			}
			if err := s.finishSpawn(c, title, req, facts, adv.Why, intent); err != nil {
				return fmt.Errorf("the spawn was not recorded in the decisions ledger, so it was not made: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		// The undo of spawnWith deleted the conversation; the rest (the draft task session, the intent) is ended here, and
		// when that fails too the intent stays, so the sweep finishes it and a repeat is refused until then.
		if cerr := s.endSpawnNow(ctx, intent); cerr != nil {
			err = fmt.Errorf("%w (cleanup unfinished, the host will retry: %v)", err, cerr)
		}
		s.recordSpawn(c, title, req, facts, AdviceRefuse, "not made: "+err.Error())
		return SessionSpawned{}, err
	}
	_ = s.clearAdvised(profile, c.ConversationID, hash)
	res := SessionSpawned{Spawned: out, TaskSessionID: made.ID, Kind: made.Kind, Title: made.Title, Depth: made.Depth}
	if req.Goal != "" {
		cmd, err := s.Enqueue(Command{ProfileID: profile, ConversationID: out.ConversationID, Kind: KindSend, TurnID: newMasterTurnID(), Message: req.Goal, Issuer: c.ConversationID})
		if err != nil {
			return res, fmt.Errorf("the session %s and its task session #%d were created but its first message was not queued: %w", out.ConversationID, made.ID, err)
		}
		res.CommandID, res.TurnID = cmd.ID, cmd.TurnID
	}
	return res, nil
}

// spawnRoom is mayHaveChildren with the reason a child gives when it has no room: it is at the working depth.
func (s *Store) spawnRoom(c Caller) (Settings, error) {
	ps, err := s.mayHaveChildren(c)
	if err == nil || !errors.Is(err, ErrNotYours) {
		return ps, err
	}
	if x, ok, e := s.GetSession(c.ProfileID, c.ConversationID); e == nil && ok && x.Role == "child" && x.State != StateStopped {
		return ps, fmt.Errorf("%w (%w): this session is at the working depth of its tree; run this work here with workers of this turn instead of a session of its own", ErrLimit, ErrAtWorkingDepth)
	}
	return ps, err
}
