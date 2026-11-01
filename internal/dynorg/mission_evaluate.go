package dynorg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// mission_evaluate and mission_reevaluate (plan 2026-10-28-mission-research-directions.md, section 4). They are the
// lead's alone (a worker or helper never has them: handle refuses every name but three for a worker, and childToolSpecs
// does not list them). Both rank the candidate directions the lead states with the pure functions of mission_rank.go and
// mission_reeval.go, write what they were given and what they decided as a note in the mission session's history, and
// return the picks. They grant nothing: they do not spawn, approve, stop or raise a budget. A dropped direction is a
// proposal the lead acts on with the session controls it already has.

const (
	ToolMissionEvaluate   = "mission_evaluate"
	ToolMissionReevaluate = "mission_reevaluate"
	// evaluationNote starts the note that records an evaluation.
	evaluationNote = "mission_evaluation "
	// recordTitleChars shortens titles in the record so that a full list fits one session event (8 KiB).
	recordTitleChars = 50
	// Bounds on what one call may carry, in bytes, checked before any text is normalized (a rune is at most 4 bytes).
	maxEvaluateArgBytes  = 512 << 10
	maxReviewerNameBytes = 100
	maxReviewerNoteBytes = 8000
	maxReasonBytes       = 300 * 4
)

// Reviewer is what a reviewer on another model said about the scoring.
type Reviewer struct {
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
	Verdict string `json:"verdict"` // agree | adjust
	Note    string `json:"note,omitempty"`
}

type evaluateArgs struct {
	Candidates []DirCandidate     `json:"candidates"`
	Weights    map[string]float64 `json:"weights"`
	K          *int               `json:"k"`
	Reviewer   *Reviewer          `json:"reviewer"`
	Reason     string             `json:"reason"`
	Current    []CurrentDirection `json:"current"`
}

func missionEvaluateSpecs() []monomind.ToolSpec {
	cand := map[string]any{"type": "array", "description": "DirCandidate directions, 1 to 20. Each: title, thesis (what it is and why it should work), evidence (references to research reports), scores (success, cost, risk, effort, each 0 to 10: success higher is better; cost, risk and effort higher is worse), and for a re-evaluation optionally merge_with (the title of a better candidate it folds into).",
		"items": map[string]any{"type": "object"}}
	weights := map[string]any{"type": "object", "description": "Weights per criterion, 0 to 100, not all 0. Default: success 3, cost 1, risk 1, effort 1."}
	k := intProp("How many directions to pick; at most the operator's setting (default 3, never above 6). Default: the operator's setting.")
	reviewer := map[string]any{"type": "object", "description": "Optional: what the reviewer on another model said about the scoring ({runtime, model, verdict: agree|adjust, note}). It is recorded; it does not change the ranking."}
	return []monomind.ToolSpec{
		{Name: ToolMissionEvaluate, Description: "Rank candidate directions against the success measure, cost, risk and effort and pick the best k. The ranking is computed by code from your scores and weights (weighted score; a tie goes to the lower risk, then the lower cost): the same input always gives the same picks. The inputs, the ranking and the picks are recorded in the mission's history. It returns the picks and a brief for a reviewer on another model to check your scoring: start that reviewer (org_spawn, role reviewer, read only) before you start any direction, and call again with its verdict. It starts, approves and funds nothing: each pick becomes a direction with session_spawn.",
			Schema: obj(map[string]any{"candidates": cand, "weights": weights, "k": k, "reviewer": reviewer}, "candidates")},
		{Name: ToolMissionReevaluate, Description: "Re-rank the directions after the host's check-in says to (a direction finished, is blocked or stalled, the budget passed 80 percent, or new research landed). Same candidates, weights and k as mission_evaluate, plus reason and current (the directions running now: title as among the candidates, session_id), all scored again. It returns keep, drop, add and merge decisions and records them. A drop or merge is only a proposal: you stop that direction yourself with the session controls you have; nothing is stopped, spawned, approved or funded here.",
			Schema: obj(map[string]any{"candidates": cand, "weights": weights, "k": k, "reviewer": reviewer,
				"reason":  str("Why now, in a line (a trigger of the check-in, or new evidence)."),
				"current": map[string]any{"type": "array", "description": "The running directions: [{title, session_id}].", "items": map[string]any{"type": "object"}}}, "candidates", "reason", "current")},
	}
}

// currentK is the operator's K now: the live reader when the chat set one (its error is the evaluation's: an unreadable
// setting is not replaced by the default), else the number the lead was configured with; unset or 0 is the default of 3,
// never above 6. x is the connection of the transaction the answer must hold in, nil outside one.
func (m *MissionConfig) currentK(ctx context.Context, x tasks.Queryer) (int, error) {
	if m == nil {
		return DefaultDirections, nil
	}
	k := m.MaxDirections
	if m.DirectionsK != nil {
		var err error
		if k, err = m.DirectionsK(ctx, x); err != nil {
			return 0, fmt.Errorf("the operator's mission_directions could not be read, so nothing was evaluated: %w", err)
		}
	}
	if k <= 0 {
		return DefaultDirections, nil
	}
	return min(k, HardMaxDirections), nil
}

// EvaluateView is mission_evaluate's result.
type EvaluateView struct {
	Ranking   []Ranked           `json:"ranking"`
	Picks     []string           `json:"picks"`
	K         int                `json:"k"`
	MaxK      int                `json:"max_k"`
	Weights   map[string]float64 `json:"weights"`
	Decisions []Decision         `json:"decisions,omitempty"`
	Review    *ReviewAsk         `json:"review,omitempty"`
	Reviewed  string             `json:"reviewer_verdict,omitempty"`
	Recorded  bool               `json:"recorded"`
	Next      string             `json:"next"`
}

// ReviewAsk is the reviewer to start and its brief.
type ReviewAsk struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Brief   string `json:"brief"`
}

func (c *Conductor) handleMissionEvaluate(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) > maxEvaluateArgBytes {
		return "", fmt.Errorf("the arguments are %d bytes: at most %d; send fewer or shorter candidates", len(args), maxEvaluateArgBytes)
	}
	var a evaluateArgs
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if err := a.checkSizes(); err != nil {
		return "", err
	}
	mc, err := c.missions()
	if err != nil {
		return "", err
	}
	m := c.cfg.Mission
	if m != nil && !m.Planning() {
		return "", fmt.Errorf("missions are not on for this conversation: the user has not confirmed one")
	}
	sid, err := c.sessionOf(ctx, mc, 0)
	if err != nil {
		return "", err
	}
	re := name == ToolMissionReevaluate
	if err := ValidateCandidates(a.Candidates); err != nil {
		return "", err
	}
	w, err := ValidateWeights(a.Weights)
	if err != nil {
		return "", err
	}
	maxK, err := m.currentK(ctx, nil)
	if err != nil {
		return "", err
	}
	k := maxK
	if a.K != nil {
		k = *a.K
	}
	if k < 1 || k > maxK {
		return "", fmt.Errorf("k is at most %d (the operator's setting mission_directions; never above %d) and at least 1", maxK, HardMaxDirections)
	}
	k = min(k, len(a.Candidates))
	if a.Reviewer != nil && a.Reviewer.Verdict != "agree" && a.Reviewer.Verdict != "adjust" {
		return "", fmt.Errorf("reviewer.verdict is agree or adjust")
	}
	view := EvaluateView{K: k, MaxK: maxK, Weights: w}
	if re {
		if strings.TrimSpace(a.Reason) == "" || len([]rune(a.Reason)) > 300 {
			return "", fmt.Errorf("reason is required, at most 300 characters")
		}
		a.Current, err = c.checkCurrent(ctx, mc, m, sid, a.Current)
		if err != nil {
			return "", err
		}
		view.Ranking, view.Decisions, err = Reevaluate(a.Candidates, w, k, a.Current)
		if err != nil {
			return "", err
		}
	} else {
		view.Ranking = Rank(a.Candidates, w, k)
	}
	for _, r := range view.Ranking {
		if r.Picked {
			view.Picks = append(view.Picks, r.Title)
		}
	}
	rev := ReviewerFor(c.cfg.Staffer)
	view.Review = &ReviewAsk{Runtime: rev.Runtime, Model: rev.Model, Brief: BuildEvaluationBrief(a.Candidates, view.Ranking, w)}
	if a.Reviewer != nil {
		view.Reviewed = a.Reviewer.Verdict
	}
	note, err := evaluationRecord(name, a, view)
	if err != nil {
		return "", fmt.Errorf("the evaluation was not recorded, so it does not stand: %w", err)
	}
	// K is read again in the record's own transaction: the evaluation stands only if no more was picked than the
	// operator allows at the moment it is written.
	stillWithinK := func(ctx context.Context, x tasks.Queryer) error {
		cur, err := m.currentK(ctx, x)
		if err != nil {
			return err
		}
		if view.K > cur {
			return fmt.Errorf("the operator lowered mission_directions to %d while this evaluation ran (it picked %d); evaluate again", cur, view.K)
		}
		if re {
			return currentStillHolds(ctx, mc.Store, x, mc.ProfileID, sid, a.Current)
		}
		return nil
	}
	if c.beforeRecord != nil {
		c.beforeRecord()
	}
	if err := mc.Store.AddSessionEventChecked(ctx, mc.ProfileID, sid, tasks.EventNote, note, missionActor(), stillWithinK); err != nil {
		return "", fmt.Errorf("the evaluation was not recorded, so it does not stand: %w", err)
	}
	view.Recorded = true
	view.Next = evaluateNext(re, view)
	return marshal(view)
}

// checkSizes bounds the text fields in bytes, before anything normalizes them.
func (a evaluateArgs) checkSizes() error {
	if len(a.Reason) > maxReasonBytes {
		return fmt.Errorf("reason is required, at most 300 characters")
	}
	if len(a.Current) > MaxCandidates {
		return fmt.Errorf("at most %d current directions", MaxCandidates)
	}
	for _, d := range a.Current {
		if len(d.Title) > maxTitleBytes {
			return fmt.Errorf("a current direction's title is over %d bytes", maxTitleBytes)
		}
	}
	if r := a.Reviewer; r != nil {
		switch {
		case len(r.Runtime) > maxReviewerNameBytes || len(r.Model) > maxReviewerNameBytes:
			return fmt.Errorf("reviewer.runtime and reviewer.model are at most %d bytes", maxReviewerNameBytes)
		case len(r.Note) > maxReviewerNoteBytes:
			return fmt.Errorf("reviewer.note is at most %d bytes", maxReviewerNoteBytes)
		case len(r.Verdict) > maxReviewerNameBytes:
			return fmt.Errorf("reviewer.verdict is agree or adjust")
		}
	}
	return nil
}

// checkCurrent refuses a re-evaluation whose "current" directions are not the lead's own open ones, and returns them
// with the titles the sessions HAVE. The session id is the identity: it must be a direction session below THIS mission,
// open (draft, running or paused, not archived) and named once. The title the lead states is a label only: a label that
// differs from the stored title is not a refusal (the stored title is used from here on, so a title the candidate cap
// let through can always be matched), unless it is the title of ANOTHER open direction, which is a swapped id. The list
// must also be complete: an open direction of this mission that is left out is refused by id (a direction the lead
// forgot would be recorded as an add, or get no decision at all). An empty list is right when none is open.
// A merge target is a candidate the lead scored (Reevaluate checks it names another one); when it is itself a running
// direction it is one of these and so has passed the same checks.
func (c *Conductor) checkCurrent(ctx context.Context, mc *MissionRun, m *MissionConfig, mission int64, current []CurrentDirection) ([]CurrentDirection, error) {
	actor := missionActor()
	if m != nil {
		actor = m.actor(0)
	}
	list, err := mc.Store.ListSessions(ctx, mc.ProfileID, tasks.SessionFilter{}, actor)
	if err != nil {
		return nil, fmt.Errorf("the sessions of this mission could not be read, so the current directions were not checked: %w", err)
	}
	byID := make(map[int64]tasks.Session, len(list))
	var open []tasks.Session
	for _, se := range list {
		byID[se.ID] = se
		if isOpenDirectionOf(se, mission) {
			open = append(open, se)
		}
	}
	openTitle := map[string]map[int64]bool{} // several open directions may share a normalized title
	for _, se := range open {
		k := titleKey(se.Title)
		if openTitle[k] == nil {
			openTitle[k] = map[int64]bool{}
		}
		openTitle[k][se.ID] = true
	}
	seen := map[int64]bool{}
	out := make([]CurrentDirection, 0, len(current))
	for _, d := range current {
		title := oneLine(d.Title)
		se, ok := byID[d.SessionID]
		switch {
		case d.SessionID <= 0:
			return nil, fmt.Errorf("current direction %q needs session_id: the number of its direction session", title)
		case seen[d.SessionID]:
			return nil, fmt.Errorf("session #%d is listed twice among the current directions", d.SessionID)
		case !ok:
			return nil, fmt.Errorf("session #%d (current direction %q) is not an open session of this profile", d.SessionID, title)
		case se.Kind != tasks.SessionKindDirection:
			return nil, fmt.Errorf("session #%d (current direction %q) is a %s session, not a direction", d.SessionID, title, se.Kind)
		case se.ParentSessionID != mission:
			return nil, fmt.Errorf("session #%d (current direction %q) is not a direction of this mission", d.SessionID, title)
		case se.State != tasks.SessionDraft && se.State != tasks.SessionRunning && se.State != tasks.SessionPaused:
			return nil, fmt.Errorf("session #%d (current direction %q) is %s: only open directions are current", d.SessionID, title, se.State)
		case se.Archived:
			return nil, fmt.Errorf("session #%d (current direction %q) is not an open session of this profile", d.SessionID, title)
		}
		if owners := openTitle[titleKey(d.Title)]; len(owners) > 0 && !owners[d.SessionID] {
			return nil, fmt.Errorf("session #%d is titled %q, not %q (that is session #%d): pass each direction with its own session id", d.SessionID, oneLine(se.Title), title, firstID(owners))
		}
		seen[d.SessionID] = true
		out = append(out, CurrentDirection{Title: se.Title, SessionID: d.SessionID})
	}
	if err := missingOpen(open, seen); err != nil {
		return nil, err
	}
	return out, nil
}

func isOpenDirectionOf(se tasks.Session, mission int64) bool {
	return se.Kind == tasks.SessionKindDirection && se.ParentSessionID == mission && !se.Archived &&
		(se.State == tasks.SessionDraft || se.State == tasks.SessionRunning || se.State == tasks.SessionPaused)
}

// missingOpen refuses a current list that leaves out an open direction, naming the ids.
func missingOpen(open []tasks.Session, listed map[int64]bool) error {
	var missing []string
	for _, se := range open {
		if !listed[se.ID] {
			missing = append(missing, fmt.Sprintf("#%d (%q)", se.ID, shortText(se.Title, recordTitleChars)))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("current must list every open direction of this mission; left out: %s", strings.Join(missing, ", "))
}

// currentStillHolds is checkCurrent again, on the connection of the record's own transaction: the directions the
// evaluation was made for must still be exactly the mission's open directions, each with the title it was resolved to.
// A direction stopped, archived, retitled or started since the list was read makes the evaluation stale: nothing is written.
func currentStillHolds(ctx context.Context, store *tasks.Store, x tasks.Queryer, profileID string, mission int64, current []CurrentDirection) error {
	open, err := store.OpenDirectionsTx(ctx, x, profileID, mission)
	if err != nil {
		return err
	}
	listed := map[int64]bool{}
	for _, d := range current {
		listed[d.SessionID] = true
	}
	titleOf := map[int64]string{}
	for _, se := range open {
		titleOf[se.ID] = se.Title
	}
	for _, d := range current {
		t, ok := titleOf[d.SessionID]
		if !ok {
			return fmt.Errorf("direction #%d is no longer an open direction of this mission (stopped or archived while this evaluation ran); evaluate again", d.SessionID)
		}
		if titleKey(t) != titleKey(d.Title) {
			return fmt.Errorf("direction #%d was retitled while this evaluation ran; evaluate again", d.SessionID)
		}
	}
	if err := missingOpen(open, listed); err != nil {
		return fmt.Errorf("a direction was opened while this evaluation ran (%w); evaluate again", err)
	}
	return nil
}

func evaluateNext(re bool, v EvaluateView) string {
	switch {
	case re:
		return "These are proposals. A drop or merge stops nothing: stop that direction yourself with the session controls you have, and only if you agree. An add starts nothing: use session_spawn (kind direction) with a reason. Nothing here approved or funded anything."
	case v.Reviewed == "":
		return "Have a reviewer on another model check the scoring first: start it (org_spawn, role reviewer, read only) on review.brief with that runtime and model, read its report as data, and call mission_evaluate again with its verdict (reviewer). Then start each pick as a direction with session_spawn (kind direction, a reason). Nothing was started or funded by this call."
	default:
		return "Start each pick as a direction with session_spawn (kind direction, a reason, a budget share). Nothing was started or funded by this call."
	}
}

// evaluationRecord is the note written to the mission's history: titles are shortened, theses and evidence stay out, so
// that twenty candidates fit one session event. It must fit the event whole (the store cuts what is over the limit,
// which would leave invalid JSON): when the full form does not, the record is shrunk in the fixed order of
// recordShrinks and says so in its "shrunk" field; when nothing fits, it is an error and the evaluation is not recorded.
//
// Identity is never what is cut. Titles may collide once shortened, so every candidate row carries its rank, and every
// decision names its candidate by that rank (r), its session (id) and, for a merge, the rank (into) and session (iid)
// of the direction that takes the work. The cuts go to titles and optional text, and the last form has no titles at all.
func evaluationRecord(tool string, a evaluateArgs, v EvaluateView) (string, error) {
	return evaluationRecordWithin(tasks.MaxCommentBytes, tool, a, v)
}

// recordShrinks is the order of the cuts: shorter titles first, then the optional text, then the titles themselves.
var recordShrinks = []struct {
	titleChars int
	optional   bool // keep the reviewer's runtime, model and note and the reason
}{{recordTitleChars, true}, {24, true}, {12, true}, {12, false}, {6, false}, {0, false}}

func evaluationRecordWithin(limit int, tool string, a evaluateArgs, v EvaluateView) (string, error) {
	for i, sh := range recordShrinks {
		note, err := buildEvaluationRecord(tool, a, v, sh.titleChars, sh.optional, i)
		if err != nil {
			return "", err
		}
		if len(note) <= limit {
			return note, nil
		}
	}
	return "", fmt.Errorf("the record of this evaluation is over %d bytes even when shrunk", limit)
}

// recordWindow is how much of a text the record looks at to make titleChars characters of it: the cut is made on bytes
// first, so the work does not depend on how long the text is.
func recordWindow(titleChars int) int { return titleChars * 16 }

// shortText is at most n characters of s with its white space folded, looking at no more than a window of its bytes.
func shortText(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if w := recordWindow(n); len(s) > w {
		s = s[:w]
		for len(s) > 0 && !utf8.RuneStart(s[len(s)-1]) {
			s = s[:len(s)-1]
		}
	}
	r := []rune(oneLine(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func buildEvaluationRecord(tool string, a evaluateArgs, v EvaluateView, titleChars int, optional bool, shrunk int) (string, error) {
	type row struct {
		T string    `json:"t,omitempty"`
		S []float64 `json:"s"` // success, cost, risk, effort
		R int       `json:"rank"`
		P bool      `json:"pick,omitempty"`
	}
	type decision struct {
		A    string `json:"a"`
		R    int    `json:"r"` // the candidate's rank in "candidates"
		T    string `json:"t,omitempty"`
		ID   int64  `json:"id,omitempty"`   // its session
		Into int    `json:"into,omitempty"` // merge: the rank of the direction that takes the work
		IID  int64  `json:"iid,omitempty"`  // and its session
	}
	short := func(s string) string { return shortText(s, titleChars) }
	scores := map[string][]float64{}
	for _, c := range a.Candidates {
		scores[titleKey(c.Title)] = []float64{c.Scores[CritSuccess], c.Scores[CritCost], c.Scores[CritRisk], c.Scores[CritEffort]}
	}
	rows := make([]row, len(v.Ranking))
	rankOf := map[string]int{}
	for i, r := range v.Ranking {
		rows[i] = row{T: short(r.Title), S: scores[titleKey(r.Title)], R: r.Rank, P: r.Picked}
		rankOf[titleKey(r.Title)] = r.Rank
	}
	rec := map[string]any{"tool": tool, "k": v.K, "weights": v.Weights, "candidates": rows}
	if shrunk > 0 {
		rec["shrunk"] = shrunk
	}
	if len(v.Decisions) > 0 {
		ds := make([]decision, len(v.Decisions))
		for i, d := range v.Decisions {
			ds[i] = decision{A: d.Action, R: rankOf[titleKey(d.Title)], T: short(d.Title), ID: d.SessionID, IID: d.IntoSessionID}
			if d.Into != "" {
				ds[i].Into = rankOf[titleKey(d.Into)]
			}
		}
		rec["decisions"] = ds
		if optional {
			rec["reason"] = shortText(a.Reason, recordTitleChars*2)
		}
	}
	if a.Reviewer != nil {
		rv := map[string]string{"verdict": a.Reviewer.Verdict}
		if optional {
			rv["runtime"], rv["model"], rv["note"] = short(a.Reviewer.Runtime), short(a.Reviewer.Model), short(a.Reviewer.Note)
		}
		rec["reviewer"] = rv
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // < > & stay one byte each: the escapes are six
	if err := enc.Encode(rec); err != nil {
		return "", err
	}
	return evaluationNote + strings.TrimSuffix(b.String(), "\n"), nil
}

// BuildEvaluationBrief is the brief for the reviewer of the scoring: the ranking by code, and every candidate's title,
// thesis and evidence references inside a fence, since the lead wrote them from reports that came from the web. Outside
// the fences there are only our own words and numbers (the rank, the scores, the weights).
func BuildEvaluationBrief(cs []DirCandidate, ranked []Ranked, w map[string]float64) string {
	f := newFence()
	var b strings.Builder
	b.WriteString("Check the scoring of candidate directions for a mission. You read; you change nothing. Reply with a numbered list of scores you think are wrong and why (with the evidence reference you rely on), or \"scoring holds\". Do not re-rank: the ranking is computed from the scores.\n\nLook for: a success score the thesis does not support, a risk or cost the thesis hides, scores that favour the first or the longest candidate, evidence that is missing or does not say what the thesis claims.\n\n")
	b.WriteString(f.preface())
	fmt.Fprintf(&b, "Scores are 0 to 10 (success: higher is better; cost, risk, effort: higher is worse). Weights: success %g, cost %g, risk %g, effort %g.\n\n", w[CritSuccess], w[CritCost], w[CritRisk], w[CritEffort])
	byKey := map[string]DirCandidate{}
	for _, c := range cs {
		byKey[titleKey(c.Title)] = c
	}
	for _, r := range ranked {
		c := byKey[titleKey(r.Title)]
		fmt.Fprintf(&b, "Candidate #%d (weighted %.2f)\n  success %g, cost %g, risk %g, effort %g\n", r.Rank,
			r.Score, c.Scores[CritSuccess], c.Scores[CritCost], c.Scores[CritRisk], c.Scores[CritEffort])
		b.WriteString("  title, thesis and evidence:\n" + f.wrap("Title: "+oneLine(c.Title)+"\n"+c.Thesis+"\n"+strings.Join(c.Evidence, "\n")) + "\n")
	}
	return b.String()
}
