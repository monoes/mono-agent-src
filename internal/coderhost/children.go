package coderhost

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
)

// Master tools: a master session creates child sessions and controls its
// descendants (docs/mastermind/plans/2026-10-26-coder-session-host.md, 3.2). The
// caller is always a Caller from a verified session token (sessiontoken.go); the
// functions here take no other notion of who is asking.

// Errors a caller can tell apart. Each is wrapped with the reason.
var (
	// ErrNotYours: the target is not a descendant of the caller (a peer, an
	// ancestor, itself, an unrelated session, or none at all: one answer for all
	// of them, so a caller cannot probe which sessions exist).
	ErrNotYours = errors.New("not one of your child sessions")
	// ErrOperatorOnly: the thing asked for belongs to the operator.
	ErrOperatorOnly = errors.New("operator only")
	// ErrLimit: a spawn limit (depth, children, rate, budget) holds.
	ErrLimit = errors.New("limit reached")
	// ErrAccess: the access level asked for is not allowed.
	ErrAccess = errors.New("access not allowed")
	// ErrInvalid: the request is malformed.
	ErrInvalid = errors.New("invalid request")
)

// MasterTurnPrefix starts the id of a turn a master made by sending to a child. It
// is not a person's send (see HostMade): it can carry no mission confirmation and
// the check-in limit counts it.
const MasterTurnPrefix = "master-"

// Spawn limits that are not settings. A master's own max_children and max_depth
// (operator-only settings) are checked as well.
const (
	// MaxSessionDepth is the deepest tree the design allows: a master (level 1) and its children (level 2).
	MaxSessionDepth = 2
	SpawnRateWindow = 10 * time.Minute
	SpawnRateMax    = 6
	maxTreeWalk     = 16
)

// AgentAdjustable lists the settings a master may change on a descendant through
// the tools. It is an allowlist: a setting added later is the operator's until it
// is named here. Everything else (OperatorOnly, role, parent, unattended, access,
// budget) is the operator's.
var AgentAdjustable = []string{"checkin_minutes", "skip_if_unchanged", "checkin_triggers", "silent_minutes", "active_hours", "mesh_visible", "mesh_alias"}

// newMasterTurnID makes the id of a master-made turn.
func newMasterTurnID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return MasterTurnPrefix + hex.EncodeToString(b)
}

// SpawnRequest is what a master asks for. Everything but Message may be left out:
// the child takes the runtime, model and folder of the parent, and an access level
// of the parent's, never above workspace.
type SpawnRequest struct {
	Message   string  // the child's first message (optional)
	Access    string  // coding or workspace, at most the parent's; "" = the parent's, capped at workspace
	Cwd       string  // a folder inside the parent's (default: the parent's)
	BudgetUSD float64 // carved out of the parent's remaining budget (0 = no cap of its own, only when the parent has none)
}

// Spawned is the new child.
type Spawned struct {
	ConversationID string  `json:"conversationId"`
	Access         string  `json:"access"`
	Cwd            string  `json:"cwd"`
	BudgetUSD      float64 `json:"budgetUsd"`
	CommandID      int64   `json:"commandId,omitempty"`
	TurnID         string  `json:"turnId,omitempty"`
}

// level is how deep a session sits: 1 for a root, 2 for its child. ok is false
// when the chain is broken or loops.
func (s *Store) level(profileID, conversationID string) (int, bool) {
	seen := map[string]bool{}
	cur := conversationID
	for lvl := 1; lvl <= maxTreeWalk; lvl++ {
		if seen[cur] {
			return 0, false
		}
		seen[cur] = true
		x, ok, err := s.GetSession(profileID, cur)
		if err != nil || !ok {
			return 0, false
		}
		if x.Parent == "" {
			return lvl, true
		}
		cur = x.Parent
	}
	return 0, false
}

// IsDescendant says whether target lies below ancestor in the tree, walking up
// from target through the parent links in the database (a cycle or a missing link
// ends the walk with false). A session is not its own descendant.
func (s *Store) IsDescendant(profileID, ancestor, target string) bool {
	below, _ := s.IsDescendantErr(profileID, ancestor, target)
	return below
}

// IsDescendantErr is IsDescendant that also says when a session could not be read (the answer is
// then false with the error), so a caller that holds on a failed read can tell it from "not below".
func (s *Store) IsDescendantErr(profileID, ancestor, target string) (bool, error) {
	if ancestor == "" || target == "" || ancestor == target {
		return false, nil
	}
	seen := map[string]bool{target: true}
	cur := target
	for i := 0; i < maxTreeWalk; i++ {
		x, ok, err := s.GetSession(profileID, cur)
		if err != nil {
			return false, err
		}
		if !ok || x.Parent == "" {
			return false, nil
		}
		if x.Parent == ancestor {
			return true, nil
		}
		if seen[x.Parent] {
			return false, nil
		}
		seen[x.Parent] = true
		cur = x.Parent
	}
	return false, nil
}

func capAccess(parent string) string {
	if ai.AccessRank(parent) > ai.AccessRank(ai.AccessWorkspace) {
		return ai.AccessWorkspace
	}
	return parent
}

// childAccess decides the child's access level: never above the parent's, never
// full, never scoped (a session needs a folder).
func childAccess(parent, asked string) (string, error) {
	if ai.AccessRank(parent) < ai.AccessRank(ai.AccessCoding) {
		return "", fmt.Errorf("%w: the master's access (%s) cannot run child sessions", ErrAccess, parent)
	}
	if asked == "" {
		return capAccess(parent), nil
	}
	if !ai.ValidAccess(asked) {
		return "", fmt.Errorf("%w: access %q is not a level", ErrInvalid, asked)
	}
	switch {
	case asked == ai.AccessFull:
		return "", fmt.Errorf("%w: a spawned session never gets full access; the operator grants it", ErrAccess)
	case ai.AccessRank(asked) < ai.AccessRank(ai.AccessCoding):
		return "", fmt.Errorf("%w: a session needs coding or workspace access", ErrAccess)
	case ai.AccessRank(asked) > ai.AccessRank(parent):
		return "", fmt.Errorf("%w: a child cannot have more access (%s) than its master (%s)", ErrAccess, asked, parent)
	}
	return asked, nil
}

// childCwd resolves the folder of a child: the parent's, or a folder inside it.
func childCwd(parentCwd, asked string) (string, error) {
	if parentCwd == "" {
		return "", fmt.Errorf("%w: the master has no folder", ErrInvalid)
	}
	if asked == "" {
		return parentCwd, nil
	}
	p := asked
	if !filepath.IsAbs(p) {
		p = filepath.Join(parentCwd, p)
	}
	root, err := filepath.EvalSymlinks(parentCwd)
	if err != nil {
		return "", fmt.Errorf("%w: the master's folder is not readable", ErrInvalid)
	}
	got, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%w: folder %q does not exist", ErrInvalid, asked)
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%w: %q is not a folder", ErrInvalid, asked)
	}
	if rel, err := filepath.Rel(root, got); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: the folder must be inside the master's folder", ErrAccess)
	}
	return got, nil
}

// Carved is the budget the children of a session hold; Remaining is what the
// session can still hand out (-1 when it has no cap).
func (s *Store) Carved(profileID, conversationID string) (carved float64, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(budget_usd), 0) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ?`, profileID, conversationID).Scan(&carved)
	return carved, err
}

// Budget returns a session's cap (0 = none).
func (s *Store) Budget(profileID, conversationID string) (float64, error) {
	var b float64
	err := s.db.QueryRow(`SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&b)
	return b, err
}

// SetBudget sets a session's cap (the operator's; 0 = none). A cap cannot be set
// below what the session already handed to its children.
func (s *Store) SetBudget(profileID, conversationID string, usd float64) error {
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return fmt.Errorf("%w: the budget must be a number of at least 0", ErrInvalid)
	}
	if _, err := s.EnsureSession(profileID, conversationID); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE coder_sessions SET budget_usd = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ?
		AND (? = 0 OR ? + 1e-9 >= (SELECT COALESCE(SUM(k.budget_usd), 0) FROM coder_sessions k WHERE k.profile_id = ? AND k.parent_conversation_id = ?))`,
		usd, s.stamp(), profileID, conversationID, usd, usd, profileID, conversationID)
	return s.applied(res, err, "the budget is below what this session already gave its children")
}

// carveBudget gives the child its budget out of the parent's, in one statement so
// two spawns cannot both take the last of it. A parent without a cap hands out any
// amount (0 = none); a parent with one hands out a positive amount that fits.
func (s *Store) carveBudget(profileID, parent, child string, usd float64) error {
	spent, err := s.Spent(profileID, parent) // read first, and part of the statement's guard: what the master has spent is not its to give
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE coder_sessions SET budget_usd = ?, updated_at = ?
		WHERE profile_id = ? AND conversation_id = ? AND parent_conversation_id = ?
		AND ( (SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?) <= 0
		   OR ( ? > 0 AND ? <= (SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?)
		        - ? - COALESCE((SELECT SUM(budget_usd) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ? AND conversation_id != ?), 0) + 1e-9 ) )`,
		usd, s.stamp(), profileID, child, parent, profileID, parent, usd, usd, profileID, parent, spent, profileID, parent, child)
	return s.applied(res, err, "")
}

// takeSpawnSlot records one spawn (an attempt: it is kept when the spawn fails after the slot is taken,
// so a failing loop cannot spawn faster than the limit) if the parent made fewer than SpawnRateMax in
// the window; one statement, so concurrent callers cannot both take the last slot.
func (s *Store) takeSpawnSlot(profileID, parent string) error {
	now := s.now().UTC()
	// rows older than the window can no longer count: drop them
	_, _ = s.db.Exec(`DELETE FROM coder_session_spawns WHERE profile_id = ? AND parent = ? AND created_at < ?`, profileID, parent, now.Add(-SpawnRateWindow).Format(timeLayout))
	res, err := s.db.Exec(`INSERT INTO coder_session_spawns (profile_id, parent, child, created_at)
		SELECT ?, ?, '', ? WHERE (SELECT COUNT(*) FROM coder_session_spawns WHERE profile_id = ? AND parent = ? AND created_at >= ?) < ?`,
		profileID, parent, now.Format(timeLayout), profileID, parent, now.Add(-SpawnRateWindow).Format(timeLayout), SpawnRateMax)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: at most %d child sessions per %s", ErrLimit, SpawnRateMax, SpawnRateWindow)
	}
	return nil
}

// Spawn creates a child session of the caller, a master. Every limit is checked
// before anything is created; the ones another caller could race (children,
// rate, budget) are also enforced by the statement that applies them, and a spawn
// that fails after the conversation exists removes it again.
func (s *Store) Spawn(c Caller, req SpawnRequest) (Spawned, error) {
	parent, ok, err := s.GetSession(c.ProfileID, c.ConversationID)
	if err != nil {
		return Spawned{}, err
	}
	if !ok || parent.Role != "master" || parent.State == StateStopped {
		return Spawned{}, fmt.Errorf("%w: only a running master session can create child sessions", ErrNotYours)
	}
	ps, err := s.GetSettings(c.ProfileID, c.ConversationID)
	if err != nil {
		return Spawned{}, err
	}
	lvl, ok := s.level(c.ProfileID, c.ConversationID)
	if !ok || lvl+1 > ps.MaxDepth || lvl+1 > MaxSessionDepth {
		return Spawned{}, fmt.Errorf("%w: depth %d would pass max_depth %d", ErrLimit, lvl+1, min(ps.MaxDepth, MaxSessionDepth))
	}
	var kids int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ? AND state != 'stopped'`,
		c.ProfileID, c.ConversationID).Scan(&kids); err != nil {
		return Spawned{}, err
	}
	if kids >= ps.MaxChildren {
		return Spawned{}, fmt.Errorf("%w: %d child sessions already (max_children is %d)", ErrLimit, kids, ps.MaxChildren)
	}
	if req.BudgetUSD < 0 || math.IsNaN(req.BudgetUSD) || math.IsInf(req.BudgetUSD, 0) {
		return Spawned{}, fmt.Errorf("%w: the budget must be a number of at least 0", ErrInvalid)
	}
	if len(req.Message) > MaxMessageBytes {
		return Spawned{}, fmt.Errorf("%w: the message is over %d bytes", ErrInvalid, MaxMessageBytes)
	}
	if err := s.checkBudget(c, req.BudgetUSD); err != nil {
		return Spawned{}, err
	}
	store, err := ai.NewAIStore(s.db)
	if err != nil {
		return Spawned{}, err
	}
	pconv, err := store.GetConversation(c.ConversationID, c.ProfileID)
	if err != nil {
		return Spawned{}, fmt.Errorf("%w: the master's conversation is gone", ErrNotYours)
	}
	access, err := childAccess(pconv.EffectiveAccess(), req.Access)
	if err != nil {
		return Spawned{}, err
	}
	cwd, err := childCwd(pconv.Cwd, req.Cwd)
	if err != nil {
		return Spawned{}, err
	}
	if err := s.takeSpawnSlot(c.ProfileID, c.ConversationID); err != nil {
		return Spawned{}, err
	}
	conv, err := store.CreateConversationAccess(c.ProfileID, pconv.Backend, pconv.WorkflowContext, pconv.RuntimeID, pconv.ProviderID, pconv.Model, access, cwd, pconv.Effort)
	if err != nil {
		return Spawned{}, err
	}
	undo := func() { _ = store.DeleteConversation(conv.ID, c.ProfileID) }
	if _, err := s.EnsureSession(c.ProfileID, conv.ID); err != nil {
		undo()
		return Spawned{}, err
	}
	if err := s.SetRole(c.ProfileID, conv.ID, "child", c.ConversationID); err != nil {
		undo()
		return Spawned{}, fmt.Errorf("%w: %v", ErrLimit, err)
	}
	if err := s.carveBudget(c.ProfileID, c.ConversationID, conv.ID, req.BudgetUSD); err != nil {
		undo()
		return Spawned{}, fmt.Errorf("%w: the budget is more than the master has left", ErrLimit)
	}
	cs := DefaultSettings()
	cs.MaxChildren = 0 // a child does not make children
	if err := s.SaveSettings(c.ProfileID, conv.ID, cs); err != nil {
		undo()
		return Spawned{}, err
	}
	_, _ = s.db.Exec(`UPDATE coder_session_spawns SET child = ? WHERE id = (SELECT MAX(id) FROM coder_session_spawns WHERE profile_id = ? AND parent = ? AND child = '')`,
		conv.ID, c.ProfileID, c.ConversationID)
	out := Spawned{ConversationID: conv.ID, Access: access, Cwd: cwd, BudgetUSD: req.BudgetUSD}
	if req.Message != "" {
		cmd, err := s.Enqueue(Command{ProfileID: c.ProfileID, ConversationID: conv.ID, Kind: KindSend, TurnID: newMasterTurnID(), Message: req.Message, Issuer: c.ConversationID})
		if err != nil {
			return out, fmt.Errorf("the child %s was created but its first message was not queued: %w", conv.ID, err)
		}
		out.CommandID, out.TurnID = cmd.ID, cmd.TurnID
	}
	return out, nil
}

// checkBudget refuses early (with the numbers) what carveBudget would refuse.
func (s *Store) checkBudget(c Caller, usd float64) error {
	have, err := s.Budget(c.ProfileID, c.ConversationID)
	if err != nil {
		return err
	}
	if have <= 0 {
		return nil // the master has no cap: the child may have none or any
	}
	carved, err := s.Carved(c.ProfileID, c.ConversationID)
	if err != nil {
		return err
	}
	spent, err := s.Spent(c.ProfileID, c.ConversationID)
	if err != nil {
		return err
	}
	if left := have - carved - spent; usd <= 0 || usd > left+1e-9 {
		return fmt.Errorf("%w: the master has $%.2f of its $%.2f budget left to give; ask for an amount up to that", ErrLimit, math.Max(left, 0), have)
	}
	return nil
}
