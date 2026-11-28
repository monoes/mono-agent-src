package coderspend

import (
	"encoding/json"
	"sort"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// CumulativeRuntime is the runtime whose reported cost can be a running total for the whole runtime session, not for
// the turn or the run: every turn of a coder conversation resumes it, as a worker's follow-up does its own (#1744).
const CumulativeRuntime = "claude"

// Totals is what one exec reported for its claude session: the cost, and the per-model token counters of its result.
//
// Claude Code restores a resumed session's totals only when that session was the last to exit in the project
// directory; otherwise the new process counts again from zero. The cost and these counters are restored together.
// The exec's input_tokens and output_tokens are the run's own, not the session's, so they are not part of this.
type Totals struct {
	CostUSD float64
	Models  map[string]chatevents.CoderModelUsage // nil = not reported
}

// sums adds the counters over the models, as monomind's runner does. ok is false when there is nothing to compare: no
// model, a counter left out or negative, or nothing but zeros.
func (t Totals) sums() (s [4]int64, ok bool) {
	for _, m := range t.Models {
		for i, n := range [4]int64{m.Input, m.Output, m.CacheRead, m.CacheCreation} {
			if m.Partial || n < 0 {
				return s, false
			}
			s[i] += n
		}
	}
	return s, s != [4]int64{}
}

// Comparable says whether t carries counters a later report can be compared with.
func (t Totals) Comparable() bool {
	_, ok := t.sums()
	return ok
}

// Added is what a session of the cumulative runtime spent between prev, its last report, and now. It is the
// difference of the costs only when now shows prev's totals were carried over, by the test monomind's runner applies
// (orgrt/cumulative-meter.js): the cost and every token counter are at least prev's. Anything else, counters missing
// on either side included, is charged whole: a doubt charges more.
//
// One case stays under-charged, by at most prev's cost: a process that started from zero and still passed prev in the
// cost and in all four counters.
func Added(prev, now Totals) float64 {
	a, had := prev.sums()
	b, has := now.sums()
	if !had || !has || now.CostUSD < prev.CostUSD {
		return now.CostUSD
	}
	for i := range a {
		if b[i] < a[i] {
			return now.CostUSD
		}
	}
	return now.CostUSD - prev.CostUSD
}

// turnReports is what one turn journaled about its lead's claude session.
type turnReports struct {
	session string // the session it ran in (its first session.bound)
	at      string // its first event's time, to take the turns in order

	own    *float64 // the largest cost its own exec reported
	result []float64
	models map[string]chatevents.CoderModelUsage
	mixed  bool // two different sets of counters: neither can be trusted
	follow *chatevents.FollowUpUsage
	// perTurn: the turn's cost is marked costWhole, nothing to subtract.
	perTurn bool
}

func (r *turnReports) addModels(m map[string]chatevents.CoderModelUsage) {
	if len(m) == 0 {
		return
	}
	if r.models != nil {
		a, _ := json.Marshal(r.models)
		b, _ := json.Marshal(m)
		r.mixed = r.mixed || string(a) != string(b)
	}
	r.models = m
}

// charge is what the turn added over prev, the previous report of its session (nil: none), and the report the next
// turn is compared with.
func (r *turnReports) charge(prev *Totals) (usd float64, end *Totals) {
	end = prev
	if r.own != nil {
		now := Totals{CostUSD: *r.own, Models: r.models}
		if r.mixed {
			now.Models = nil
		}
		usd = now.CostUSD
		if prev != nil {
			usd = Added(*prev, now)
		}
		// A turn compacted before the follow-up's report was journaled holds two result figures and only the first
		// run's counters: what its session ended at is unknown.
		for _, c := range r.result {
			if r.follow == nil && c != r.result[0] {
				now.Models = nil
			}
		}
		end = &now
	}
	if f := r.follow; f != nil {
		now := Totals{CostUSD: f.CostUSD, Models: f.Models}
		switch {
		case f.SessionID != r.session:
			usd += f.CostUSD
		case end != nil:
			usd += Added(*end, now)
			end = &now
		default:
			usd += f.CostUSD
			end = &now
		}
	}
	return usd, end
}

// turnCosts reads what the conversation's turns reported they cost: what each turn's lead added (turn -> usd) and
// each worker's figure ((turn, worker) -> usd). A turn or worker that reported no cost has no entry.
func turnCosts(q Querier, profileID, conversationID string) (map[string]float64, map[[2]string]float64, error) {
	lead := map[string]float64{}       // turn -> its largest reported total
	workers := map[[2]string]float64{} // (turn, worker) -> its largest finished cost
	reports := map[string]*turnReports{}
	ev, err := q.Query(`SELECT turn_id, type, payload, created_at FROM ai_chat_events
		WHERE profile_id = ? AND conversation_id = ? AND (type IN ('usage.updated', 'agent.finished', 'session.bound')
		OR (type = 'notice' AND instr(payload, '"coder.usage"') > 0))`, profileID, conversationID)
	if err != nil {
		return nil, nil, err
	}
	for ev.Next() {
		var turn, typ, raw, at string
		if err := ev.Scan(&turn, &typ, &raw, &at); err != nil {
			ev.Close()
			return nil, nil, err
		}
		r := reports[turn]
		if r == nil {
			r = &turnReports{at: at}
			reports[turn] = r
		}
		r.at = min(r.at, at)
		switch typ {
		case "session.bound":
			var p chatevents.SessionBoundPayload
			if r.session == "" && json.Unmarshal([]byte(raw), &p) == nil && p.Runtime == CumulativeRuntime {
				r.session = p.SessionID
			}
		case "notice":
			var p chatevents.NoticePayload
			if json.Unmarshal([]byte(raw), &p) == nil && p.Code == "coder.usage" && p.CoderUsage != nil {
				r.addModels(p.CoderUsage.Models)
			}
		default:
			var p struct {
				AgentID  string                    `json:"agentId"`
				CostUSD  *float64                  `json:"costUsd"`
				Source   string                    `json:"source"`
				FollowUp *chatevents.FollowUpUsage `json:"followUp"`
				PerTurn  bool                      `json:"costWhole"`
			}
			if json.Unmarshal([]byte(raw), &p) != nil || !validCost(p.CostUSD) {
				continue // not numeric, negative: not a cost
			}
			if typ == "agent.finished" {
				k := [2]string{turn, p.AgentID}
				if cur, seen := workers[k]; !seen || *p.CostUSD > cur {
					workers[k] = *p.CostUSD
				}
				continue
			}
			if p.AgentID != "" {
				continue
			}
			if cur, seen := lead[turn]; !seen || *p.CostUSD > cur {
				lead[turn] = *p.CostUSD
			}
			r.perTurn = r.perTurn || p.PerTurn
			if p.FollowUp != nil && validCost(&p.FollowUp.CostUSD) {
				r.follow = p.FollowUp
				continue
			}
			if r.own == nil || *p.CostUSD > *r.own {
				r.own = p.CostUSD
			}
			if p.Source == "result" {
				r.result = append(r.result, *p.CostUSD)
			}
		}
	}
	ev.Close()
	if err := ev.Err(); err != nil {
		return nil, nil, err
	}
	deltaOfResumedSessions(lead, reports)
	return lead, workers, nil
}

// deltaOfResumedSessions turns each turn's reported figure into what that turn added, for turns of the cumulative
// runtime: each report is compared with that of the previous turn of the SAME runtime session (Added), the turns taken
// in the order they were created. When that turn recorded no cost, nothing is known of where the session stood. Such a
// turn, one that bound no session and the first of a session all leave nothing to subtract: the whole figure is
// charged, so a doubt charges more, never less. A turn whose cost is marked costWhole is charged as reported, and
// leaves no total to compare a later one with.
func deltaOfResumedSessions(lead map[string]float64, reports map[string]*turnReports) {
	order := make([]string, 0, len(reports))
	for turn, r := range reports {
		if r.session != "" {
			order = append(order, turn)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if a, b := reports[order[i]].at, reports[order[j]].at; a != b {
			return a < b
		}
		return order[i] < order[j]
	})
	last := map[string]*Totals{} // session -> its latest report
	for _, turn := range order {
		r := reports[turn]
		if _, reported := lead[turn]; !reported || r.perTurn {
			delete(last, r.session)
			continue
		}
		lead[turn], last[r.session] = r.charge(last[r.session])
	}
}
