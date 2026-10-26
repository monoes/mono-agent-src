package aitask

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Access levels a task asks for. Only AccessQA is not full access.
const (
	AccessCoding    = "coding"
	AccessWorkspace = "workspace"
	AccessQA        = "qa"
)

// ValidAccess reports whether a is an access level (or "" for the kind's default).
func ValidAccess(a string) bool {
	return a == "" || a == AccessCoding || a == AccessWorkspace || a == AccessQA
}

// AccessRank orders the levels by power (qa 0, coding 1, workspace 2); an
// unknown one is -1.
func AccessRank(a string) int {
	switch a {
	case AccessQA:
		return 0
	case AccessCoding:
		return 1
	case AccessWorkspace:
		return 2
	}
	return -1
}

// FullAccess reports whether a task at this access level can change anything
// the user can: every level but qa.
func FullAccess(a string) bool { return a != AccessQA }

// Kind is one sort of task: what the agent is asked and the shape of the
// result it hands back.
type Kind struct {
	Name string
	// Description is one line saying what the kind does, listed with the kinds.
	Description string
	// Check, when set, is called by Service.Complete after the schema check
	// and before the task is closed, with the task's profile and the result.
	// Its error refuses the result (schema_invalid) so the agent can fix it and
	// complete again; a panic or a run past CheckTimeout refuses too.
	Check func(ctx context.Context, profile string, raw json.RawMessage) error
	// Schema is the JSON Schema (the subset Validate checks) the result must
	// match; nil accepts any JSON object.
	Schema map[string]any
	// PromptTemplate is the kind's part of the first turn: a text/template over
	// PromptData. Empty means DefaultTemplate.
	PromptTemplate string
	// DefaultAccess is the access level when submit names none.
	DefaultAccess string
	// MaxAccess is the most a submit may ask for. Empty means the kind's
	// default: a submit can lower the level but not raise it.
	MaxAccess string
	// UnattendedOK says the kind may run with --unattended (and then only at
	// the qa access level).
	UnattendedOK bool
}

// Ceiling is the highest access level a task of this kind may have.
func (k Kind) Ceiling() string {
	switch {
	case k.MaxAccess != "":
		return k.MaxAccess
	case k.DefaultAccess != "":
		return k.DefaultAccess
	}
	return AccessCoding
}

var kindName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

var (
	kindsMu sync.RWMutex
	kinds   = map[string]Kind{}
)

// Register adds k to the registry; other packages call it from init. It
// panics on a bad or repeated name: both are programming errors.
func Register(k Kind) {
	if !kindName.MatchString(k.Name) {
		panic(fmt.Sprintf("aitask: bad kind name %q", k.Name))
	}
	if !ValidAccess(k.DefaultAccess) {
		panic(fmt.Sprintf("aitask: kind %s has bad default access %q", k.Name, k.DefaultAccess))
	}
	if !ValidAccess(k.MaxAccess) {
		panic(fmt.Sprintf("aitask: kind %s has bad max access %q", k.Name, k.MaxAccess))
	}
	if AccessRank(k.Ceiling()) < AccessRank(k.DefaultAccess) {
		panic(fmt.Sprintf("aitask: kind %s has max access %q below its default %q", k.Name, k.MaxAccess, k.DefaultAccess))
	}
	kindsMu.Lock()
	defer kindsMu.Unlock()
	if _, dup := kinds[k.Name]; dup {
		panic("aitask: kind registered twice: " + k.Name)
	}
	kinds[k.Name] = k
}

// Lookup returns the kind called name, or an unknown_kind error naming the
// kinds there are.
func Lookup(name string) (Kind, error) {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	if k, ok := kinds[name]; ok {
		return k, nil
	}
	return Kind{}, &Error{Code: CodeUnknownKind,
		Message: fmt.Sprintf("unknown task kind %q (kinds: %v)", name, namesLocked()),
		Fields:  map[string]any{"kinds": namesLocked(), "descriptions": descriptionsLocked()}}
}

// KindInfo is a kind as listed to a caller: `aitask kinds` and the agent API's
// kinds route (the prompt template is not part of it).
type KindInfo struct {
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	DefaultAccess string         `json:"defaultAccess,omitempty"`
	UnattendedOK  bool           `json:"unattendedOk"`
	Schema        map[string]any `json:"schema,omitempty"`
}

// KindInfos lists the registered kinds, sorted by name.
func KindInfos() []KindInfo {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	out := make([]KindInfo, 0, len(kinds))
	for _, n := range namesLocked() {
		k := kinds[n]
		out = append(out, KindInfo{Name: k.Name, Description: k.Description, DefaultAccess: k.DefaultAccess,
			UnattendedOK: k.UnattendedOK, Schema: k.Schema})
	}
	return out
}

// Names are the registered kinds, sorted.
func Names() []string {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	return namesLocked()
}

func descriptionsLocked() map[string]string {
	out := make(map[string]string, len(kinds))
	for n, k := range kinds {
		if k.Description != "" {
			out[n] = k.Description
		}
	}
	return out
}

// All returns the registered kinds, sorted by name. It is read-only: the Kind
// values are copies.
func All() []Kind {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	names := namesLocked()
	out := make([]Kind, 0, len(names))
	for _, n := range names {
		out = append(out, kinds[n])
	}
	return out
}

func namesLocked() []string {
	out := make([]string, 0, len(kinds))
	for n := range kinds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() {
	Register(Kind{
		Name:          "generic",
		Description:   "Do whatever the goal asks in a coder conversation and report a summary.",
		DefaultAccess: AccessCoding,
		MaxAccess:     AccessWorkspace,
		PromptTemplate: "Do what the goal asks. When you are done, hand back a JSON object with a `summary` " +
			"(what you did and what you found) and, when there is one, the `output` it produced.",
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary"},
			"properties": map[string]any{
				"summary": map[string]any{"type": "string"},
			},
		},
	})
}
