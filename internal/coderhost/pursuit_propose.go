package coderhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/novelty"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

// From the judge's answer to work (S5a). An applied round may carry next_actions, which are model text. The host never passes
// that text on: it reads each next action only to find a goal criterion the person confirmed, by its exact id ("c2"), and
// proposes a direction whose title and rationale the HOST wrote from that id. The proposal goes through the same intake as any
// other (validation, the novelty gate, the ledger, insert-only) and becomes a direction in state "proposed". Nothing here
// creates a session, a task, an approval or a budget: the lead decides whether to act, with its own tools and the usual
// approval classes (plan_approval stays hard). When the lead later starts a direction session, the host (not the lead, not
// the judge) pairs the oldest proposed direction with it, which is what lets the scorer score it.

const (
	maxProposalsPerEpoch     = 3 // the most proposals read from one round; only the first one the gate has not seen is submitted
	submittedPerEpoch        = 1 // the novelty gate lets one unchecked proposal through per epoch (no memory corpus), so one is submitted
	maxOpenPursuitDirections = 8 // no proposal while this many directions are open: the lead has enough to work on
	pursuitProposer          = "host"
)

// directionTitle is the host-written title for a criterion. One title per criterion: the novelty gate refuses it a second
// time, so a criterion gets one proposed direction for the life of the pursuit.
func directionTitle(criterion string) string { return "Work toward criterion " + criterion }

// proposalsOf turns the next actions of a judgment into proposals: one per goal criterion an action names by its exact id,
// in the order named, at most maxProposalsPerEpoch. Actions that name none are dropped.
func proposalsOf(j *types.Judgment, goal missionpursuit.Goal, epochSeq int64) []types.Proposal {
	if j == nil {
		return nil
	}
	known := map[string]bool{}
	for _, c := range goal.Criteria {
		known[c.ID] = true
	}
	seen := map[string]bool{}
	var out []types.Proposal
	for _, a := range j.NextActions {
		for _, tok := range strings.FieldsFunc(a.Text, notIDChar) {
			if !known[tok] || seen[tok] {
				continue
			}
			seen[tok] = true
			out = append(out, types.Proposal{Title: directionTitle(tok), Rationale: "Proposed by the host after judge round " + strconv.FormatInt(epochSeq, 10) + " for this goal criterion.",
				CriterionIDs: []string{tok}, ProposerSession: pursuitProposer})
			break
		}
		if len(out) >= maxProposalsPerEpoch {
			break
		}
	}
	return out
}

// notIDChar splits model text at everything but ASCII letters and digits, so that a criterion id is found only as a whole word.
func notIDChar(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
}

// pursuitCorpus is the novelty gate's corpus over the mission strategy store. Memory and knowledge are not reachable from
// here, so the gate lets at most one unchecked proposal through per epoch.
type pursuitCorpus struct {
	st      *strategy.Store
	profile string
}

var errNoSimilarity = errors.New("pursuit: memory and knowledge are not reachable")

func (c pursuitCorpus) LedgerKey(ctx context.Context, key [32]byte) (novelty.Match, bool, error) {
	n, ok, err := c.st.FindNovelty(ctx, c.profile, 1, key)
	if err != nil || !ok {
		return novelty.Match{}, false, err
	}
	id := n.DirectionID
	if id == "" {
		id = "ledger:" + hex.EncodeToString(key[:6])
	}
	return novelty.Match{ID: id}, true, nil
}

func (c pursuitCorpus) DirectionKey(ctx context.Context, key [32]byte) (novelty.Match, bool, error) {
	all, err := c.st.ListDirections(ctx, c.profile, 1)
	if err != nil {
		return novelty.Match{}, false, err
	}
	for _, d := range all {
		if d.KeyHash == key && (policy.AcceptsNewWork(d.State) || d.State == types.DirPruned) {
			return novelty.Match{ID: d.ID}, true, nil
		}
	}
	return novelty.Match{}, false, nil
}

// unseen keeps the first n proposals whose key the run has not met (no ledger row, no open or pruned direction).
func (c pursuitCorpus) unseen(ctx context.Context, props []types.Proposal, n int) []types.Proposal {
	var out []types.Proposal
	for _, p := range props {
		if len(out) >= n {
			break
		}
		_, key, err := novelty.Key(p.Title)
		if err != nil {
			continue
		}
		if _, seen, err := c.LedgerKey(ctx, key); err != nil || seen {
			continue
		}
		if _, seen, err := c.DirectionKey(ctx, key); err != nil || seen {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (pursuitCorpus) Similar(context.Context, string) ([]novelty.Hit, error) {
	return nil, errNoSimilarity
}

func newDirectionID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "d-" + hex.EncodeToString(b[:]), nil
}

// pursuitPropose submits the proposals an applied round earned. A refusal of any kind (not running, over the caps, a title
// the gate has seen) is simply no direction; the round's verdicts are already applied and nothing here is retried.
func (h *Host) pursuitPropose(ctx context.Context, rd *inputs.Reader, m pursuitMission, goal missionpursuit.Goal, res epoch.TickResult) {
	if res.Outcome != epoch.OutcomeApplied || res.Judgment == nil {
		return
	}
	props := proposalsOf(res.Judgment, goal, res.EpochSeq)
	if len(props) == 0 {
		return
	}
	prof := strconv.FormatInt(m.ID, 10)
	st := strategy.NewStore(h.Store.db)
	open, err := st.OpenDirections(ctx, prof, 1)
	if err != nil || len(open) >= maxOpenPursuitDirections {
		return
	}
	corpus := pursuitCorpus{st: st, profile: prof}
	// The gate lets one unchecked proposal through per round and writes every other one to its ledger as refused, which would
	// shut that criterion out for good. So the criteria that already have a direction or a ledger row are skipped here, and one
	// new one is submitted: each round advances the next criterion.
	props = corpus.unseen(ctx, props, submittedPerEpoch)
	if len(props) == 0 {
		return
	}
	gate, err := novelty.New(corpus, 0)
	if err != nil {
		return
	}
	p := policy.Proposer{Profile: prof, Generation: 1, Gate: gate, Ledger: st, Run: rd, NewID: newDirectionID}
	if _, _, err := p.Submit(ctx, res.EpochSeq, 0, props); err != nil {
		h.logf("coder host: pursuit of session %d: proposing directions: %v", m.ID, err)
	}
}

// Known limit (scoring only): the pairing cannot tell which session the lead meant, so an older, still unclaimed direction session
// can take a newer proposal; the scorer then scores that session's tasks for it. Nothing else follows from it.
//
// pursuitBind pairs proposed directions that have no session with direction sessions of this mission (running or ended: a session that ended before the pass still takes its direction, or the later
// sessions would be paired with the wrong ones) that no direction has claimed, oldest with lowest id. The session must be a 'direction' session of this mission and profile (the scorer checks the
// same again before it reads any fact). The pairing is the host's alone: the lead cannot name which session a direction means.
func (h *Host) pursuitBind(ctx context.Context, m pursuitMission) {
	prof := strconv.FormatInt(m.ID, 10)
	st := strategy.NewStore(h.Store.db)
	proposed, err := st.ListDirections(ctx, prof, 1, types.DirProposed)
	if err != nil {
		return
	}
	var free []types.Direction
	for _, d := range proposed {
		if d.SessionID == "" {
			free = append(free, d)
		}
	}
	if len(free) == 0 {
		return
	}
	sort.Slice(free, func(i, j int) bool {
		if free[i].CreatedEpoch != free[j].CreatedEpoch {
			return free[i].CreatedEpoch < free[j].CreatedEpoch
		}
		return free[i].ID < free[j].ID
	})
	rows, err := h.Store.db.QueryContext(ctx, `SELECT s.id FROM task_sessions s WHERE s.kind = 'direction' AND s.profile_id = ? AND s.parent_session_id = ? AND s.archived_at = ''
		AND CAST(s.id AS TEXT) NOT IN (SELECT lead_session FROM mission_strategy_directions WHERE session_id = ? AND lead_session <> '')
		ORDER BY s.id LIMIT ?`, m.Profile, m.ID, m.ID, len(free))
	if err != nil {
		return
	}
	var sessions []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			sessions = append(sessions, id)
		}
	}
	rows.Close()
	for i := 0; i < len(sessions) && i < len(free); i++ {
		if err := st.MoveDirection(ctx, prof, 1, free[i].ID, types.DirActive, strconv.FormatInt(sessions[i], 10)); err != nil {
			h.logf("coder host: pursuit of session %d: linking direction %s: %v", m.ID, free[i].ID, err)
			return
		}
	}
}
