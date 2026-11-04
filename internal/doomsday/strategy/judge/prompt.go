package judge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// CriterionInput is the host's view of one success criterion. Text and Evidence are the charter's and workers'
// words: untrusted. LastStatus is the previous judgment's reading, "" when none.
type CriterionInput struct {
	ID         string
	Text       string
	Evidence   string
	LastStatus types.CriterionStatus
}

// DirectionInput is one row of the direction table.
type DirectionInput struct {
	ID      string
	Title   string
	State   types.DirectionState
	Summary string
}

// Input is everything a judging round is given. It carries text only: no profile, token, vault or approval
// material belongs here, and the builder copies nothing else into the prompt.
type Input struct {
	Criteria   []CriterionInput
	Directions []DirectionInput
}

// Request is what the Model receives.
type Request struct {
	System   string
	User     string
	MaxBytes int // the largest answer the host will accept; a Model should stop reading there
}

type promptDoc struct {
	Criteria   []promptCriterion `json:"criteria"`
	Directions []promptDirection `json:"directions"`
}

type promptCriterion struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Evidence   string `json:"evidence,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
}

type promptDirection struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Summary string `json:"summary,omitempty"`
}

// clean makes untrusted text safe to embed: invalid UTF-8 and control characters (other than newline and tab) become
// spaces, then it is cut to max bytes on a rune boundary. JSON encoding later escapes quotes and angle brackets, so
// the text can never close the data document or open a second one.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max - len(truncationMark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncationMark
}

// Expect returns the id sets a judgment is validated against.
func (in Input) Expect() types.Expect {
	e := types.Expect{}
	for _, c := range in.Criteria {
		e.CriterionIDs = append(e.CriterionIDs, c.ID)
	}
	for _, d := range in.Directions {
		e.DirectionIDs = append(e.DirectionIDs, d.ID)
	}
	return e
}

func validEnum(s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

// check refuses an input that could never yield a valid judgment.
func (in Input) check() error {
	if len(in.Criteria) > maxCriteria || len(in.Directions) > maxDirections {
		return fmt.Errorf("%w: %d criteria, %d directions", ErrBadInput, len(in.Criteria), len(in.Directions))
	}
	seen := map[string]bool{}
	for _, c := range in.Criteria {
		if c.ID == "" || len(c.ID) > maxIDLen || !utf8.ValidString(c.ID) || seen["c:"+c.ID] {
			return fmt.Errorf("%w: criterion id", ErrBadInput)
		}
		seen["c:"+c.ID] = true
		if c.LastStatus != "" && !validEnum(string(c.LastStatus), string(types.CritNotStarted), string(types.CritProgressing),
			string(types.CritPlausiblyMet), string(types.CritRegressed)) {
			return fmt.Errorf("%w: criterion status", ErrBadInput)
		}
	}
	for _, d := range in.Directions {
		if d.ID == "" || len(d.ID) > maxIDLen || !utf8.ValidString(d.ID) || seen["d:"+d.ID] {
			return fmt.Errorf("%w: direction id", ErrBadInput)
		}
		seen["d:"+d.ID] = true
		if !validEnum(string(d.State), string(types.DirProposed), string(types.DirActive), string(types.DirStalled),
			string(types.DirFinished), string(types.DirPruned)) {
			return fmt.Errorf("%w: direction state", ErrBadInput)
		}
	}
	return nil
}

// BuildPrompt renders the request deterministically: the same Input always gives the same bytes, entries are sorted by
// id, and every field is cleaned and capped. The fixed system text carries the rules; the user text is one JSON
// document of data.
func BuildPrompt(in Input) (Request, error) {
	if err := in.check(); err != nil {
		return Request{}, err
	}
	doc := promptDoc{Criteria: make([]promptCriterion, 0, len(in.Criteria)), Directions: make([]promptDirection, 0, len(in.Directions))}
	for _, c := range in.Criteria {
		doc.Criteria = append(doc.Criteria, promptCriterion{ID: c.ID, Text: clean(c.Text, MaxFieldText),
			Evidence: clean(c.Evidence, MaxFieldText), LastStatus: string(c.LastStatus)})
	}
	for _, d := range in.Directions {
		doc.Directions = append(doc.Directions, promptDirection{ID: d.ID, Title: clean(d.Title, maxTitleLen),
			State: string(d.State), Summary: clean(d.Summary, MaxFieldText)})
	}
	sort.Slice(doc.Criteria, func(i, j int) bool { return doc.Criteria[i].ID < doc.Criteria[j].ID })
	sort.Slice(doc.Directions, func(i, j int) bool { return doc.Directions[i].ID < doc.Directions[j].ID })
	body, err := json.Marshal(doc)
	if err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	if len(body)+len(systemPrompt) > MaxPromptBytes {
		return Request{}, fmt.Errorf("%w: prompt too large", ErrBadInput)
	}
	return Request{System: systemPrompt, User: string(body), MaxBytes: types.MaxJudgmentBytes}, nil
}
