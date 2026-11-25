package surfaces

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// UIRef kinds a system surface is recorded under (chatevents.UIRef.Kind).
const (
	RefHIL           = "hil"
	RefSend          = "send"
	RefAsk           = "ask"
	RefMissionPlan   = "mission"
	RefMissionReport = "mission-report"
)

// Command is what the host should do for a validated action. Route never
// performs it.
type Command struct {
	Kind   string // the action name: hil.approve, hil.reject, mission.approve, mission.edit, mission.stop, mission.close, system.answer
	ID     string // the record id, equal to the ref's
	Fields map[string]string
}

type route struct {
	refs   []string
	idKey  string
	fields func(k string) bool // nil: no fields allowed
}

var answerKey = regexp.MustCompile(`^q[0-2]$`)

var routes = map[string]route{
	"hil.approve":     {[]string{RefHIL, RefSend}, "id", func(string) bool { return true }},
	"hil.reject":      {[]string{RefHIL, RefSend}, "id", nil},
	"mission.approve": {[]string{RefMissionPlan}, "sessionId", nil},
	"mission.edit":    {[]string{RefMissionPlan}, "sessionId", nil},
	"mission.stop":    {[]string{RefMissionPlan}, "sessionId", nil},
	"mission.close":   {[]string{RefMissionReport}, "sessionId", nil},
	"system.answer":   {[]string{RefAsk}, "questionId", answerKey.MatchString},
}

func refSurface(kind, id string) string {
	return map[string]string{RefHIL: "hil-", RefSend: "send-", RefAsk: "ask-",
		RefMissionPlan: "mission-", RefMissionReport: "mission-report-"}[kind] + id
}

// Route turns a renderer action into a Command, given the record the
// surface was built from. It refuses an unknown action name, an action for a
// different kind of record, a surface id that is not the record's surface, a
// context id that is not the record's id (a forged id), a missing id, a field
// the action does not take, and any value that is not a string (a list of at
// most one string is accepted for a single-choice answer). It runs no side
// effect.
func Route(a a2ui.Action, ref chatevents.UIRef) (Command, error) {
	r, ok := routes[a.Name]
	if !ok {
		return Command{}, fmt.Errorf("surfaces: %q is not a host action", a.Name)
	}
	known := false
	for _, k := range r.refs {
		known = known || k == ref.Kind
	}
	if !known {
		return Command{}, fmt.Errorf("surfaces: action %q does not apply to a %q record", a.Name, ref.Kind)
	}
	if ref.ID == "" {
		return Command{}, errors.New("surfaces: the record has no id")
	}
	if want := refSurface(ref.Kind, ref.ID); a.SurfaceID != want {
		return Command{}, fmt.Errorf("surfaces: action came from surface %q, not %q", a.SurfaceID, want)
	}
	id, isStr := a.Context[r.idKey].(string)
	if !isStr || id != ref.ID {
		return Command{}, fmt.Errorf("surfaces: context %s does not match the record (forged or missing id)", r.idKey)
	}
	lim := a2ui.DefaultLimits
	cmd := Command{Kind: a.Name, ID: ref.ID, Fields: map[string]string{}}
	keys := make([]string, 0, len(a.Context))
	for k := range a.Context {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == r.idKey {
			continue
		}
		if r.fields == nil || !r.fields(k) {
			return Command{}, fmt.Errorf("surfaces: action %q takes no field %q", a.Name, k)
		}
		s, err := contextString(a.Context[k], a.Name == "system.answer")
		if err != nil {
			return Command{}, fmt.Errorf("surfaces: field %q: %w", k, err)
		}
		if len(s) > lim.MaxReturnedString {
			return Command{}, fmt.Errorf("surfaces: field %q is %d bytes, over the %d byte cap", k, len(s), lim.MaxReturnedString)
		}
		cmd.Fields[k] = s
	}
	if len(cmd.Fields) > lim.MaxOptions {
		return Command{}, errors.New("surfaces: too many fields")
	}
	return cmd, nil
}

func contextString(v any, allowChoice bool) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case []any:
		if allowChoice && len(t) <= 1 {
			if len(t) == 0 {
				return "", nil
			}
			if s, ok := t[0].(string); ok {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("must be a string, not %T", v)
}
