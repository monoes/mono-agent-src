package dynorg

import (
	"fmt"
)

// Actions of a re-evaluation. Every one is a proposal: nothing is stopped, spawned, approved or funded here.
const (
	ActKeep  = "keep"
	ActDrop  = "drop"
	ActAdd   = "add"
	ActMerge = "merge"
)

// CurrentDirection is a direction that is running now: its title as it appears among the candidates, and its session.
type CurrentDirection struct {
	Title     string `json:"title"`
	SessionID int64  `json:"session_id,omitempty"`
}

// Decision is one proposal of a re-evaluation.
type Decision struct {
	Action        string `json:"action"`
	Title         string `json:"title"`
	SessionID     int64  `json:"session_id,omitempty"`
	Into          string `json:"into,omitempty"` // merge: the direction that takes its work
	IntoSessionID int64  `json:"into_session_id,omitempty"`
}

// Reevaluate ranks the candidates (the running directions among them, scored again) and says, against the best k that
// are not folded into a better one, which running directions to keep, which to drop or merge, and which candidates to
// add. It reads and returns values only.
func Reevaluate(cs []DirCandidate, w map[string]float64, k int, current []CurrentDirection) ([]Ranked, []Decision, error) {
	byKey := map[string]DirCandidate{}
	for _, c := range cs {
		byKey[titleKey(c.Title)] = c
	}
	if len(current) > MaxCandidates {
		return nil, nil, fmt.Errorf("at most %d current directions", MaxCandidates)
	}
	// live holds the first direction of each normalized title. Later ones with the same title (distinct sessions) are
	// duplicates of it: they follow its fate (merged into it when it is kept, dropped when it is not).
	live := map[string]CurrentDirection{}
	isDup := make([]bool, len(current))
	sessions := map[int64]bool{}
	for i, d := range current {
		key := titleKey(d.Title)
		if _, ok := byKey[key]; !ok {
			return nil, nil, fmt.Errorf("current direction %q is not among the candidates: score it again with the others", oneLine(d.Title))
		}
		if d.SessionID < 0 {
			return nil, nil, fmt.Errorf("current direction %q has a bad session id", oneLine(d.Title))
		}
		if first, same := live[key]; same {
			if d.SessionID == 0 || first.SessionID == 0 || sessions[d.SessionID] {
				return nil, nil, fmt.Errorf("current direction %q is listed twice", oneLine(d.Title))
			}
			isDup[i] = true
		} else {
			live[key] = d
		}
		sessions[d.SessionID] = true
	}
	for _, c := range cs {
		if c.MergeWith == "" {
			continue
		}
		if _, ok := byKey[titleKey(c.MergeWith)]; !ok || titleKey(c.MergeWith) == titleKey(c.Title) {
			return nil, nil, fmt.Errorf("candidate %q: merge_with must name another candidate", oneLine(c.Title))
		}
	}
	ranked := Rank(cs, w, 0)
	place := map[string]int{}
	for i, r := range ranked {
		place[titleKey(r.Title)] = i
	}
	// A candidate folds into the one it names when that one ranks higher; the chain ends at one that does not fold.
	finalOf := func(key string) (string, bool) {
		folded := false
		for n := 0; n <= len(cs); n++ {
			t := titleKey(byKey[key].MergeWith)
			if t == "" || place[t] >= place[key] {
				return key, folded
			}
			key, folded = t, true
		}
		return key, folded
	}
	desired := map[string]bool{}
	for i := range ranked {
		key := titleKey(ranked[i].Title)
		if _, folded := finalOf(key); folded || len(desired) >= k {
			continue
		}
		desired[key] = true
		ranked[i].Picked = true
	}
	var out []Decision
	for _, r := range ranked {
		key := titleKey(r.Title)
		if !desired[key] {
			continue
		}
		if d, ok := live[key]; ok {
			out = append(out, Decision{Action: ActKeep, Title: r.Title, SessionID: d.SessionID})
		} else {
			out = append(out, Decision{Action: ActAdd, Title: r.Title})
		}
	}
	for i, d := range current {
		key := titleKey(d.Title)
		if desired[key] {
			if isDup[i] {
				out = append(out, Decision{Action: ActMerge, Title: byKey[key].Title, SessionID: d.SessionID, Into: byKey[key].Title, IntoSessionID: live[key].SessionID})
			}
			continue
		}
		title := byKey[key].Title
		if into, folded := finalOf(key); folded && desired[into] {
			out = append(out, Decision{Action: ActMerge, Title: title, SessionID: d.SessionID, Into: byKey[into].Title, IntoSessionID: live[into].SessionID})
			continue
		}
		out = append(out, Decision{Action: ActDrop, Title: title, SessionID: d.SessionID})
	}
	return ranked, out, nil
}

// firstID is the lowest id in a set, for a stable message.
func firstID(ids map[int64]bool) int64 {
	var low int64
	for id := range ids {
		if low == 0 || id < low {
			low = id
		}
	}
	return low
}
