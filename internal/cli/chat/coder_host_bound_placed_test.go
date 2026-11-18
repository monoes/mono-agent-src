package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The bound launcher accepts its own pipe, and the one substitution a placement under the run's anchor makes: the
// duplicate in the new parent, announced by its number and proven to be the same pipe (#1723).
func TestPlacedPipeKeptAcceptsTheOwnPipeAndTheAnchorsDuplicate(t *testing.T) {
	isPipe := func(h uintptr) bool { return h == 1012 }
	if err := placedPipeKept(12, []uintptr{12}, false, []string{"A=1", coderhost.CredFDEnv + "=12"}, isPipe); err != nil {
		t.Fatalf("the launcher's own pipe: %v", err)
	}
	if err := placedPipeKept(12, []uintptr{1012}, true, []string{strings.ToLower(coderhost.CredFDEnv) + "=1012"}, isPipe); err != nil {
		t.Fatalf("the anchor's duplicate: %v", err)
	}
}

// A dropped or added handle, a wrong or doubled announcement, a replaced handle without a new parent, and an entry the
// new parent holds that is not the pipe are all refused.
func TestPlacedPipeKeptRefusesEverythingElse(t *testing.T) {
	isPipe := func(h uintptr) bool { return h == 1012 }
	ann := func(n string) []string { return []string{coderhost.CredFDEnv + "=" + n} }
	for name, c := range map[string]struct {
		list       []uintptr
		reparented bool
		env        []string
	}{
		"dropped":                         {nil, true, ann("1012")},
		"added":                           {[]uintptr{1012, 77}, true, ann("1012")},
		"announcement not rewritten":      {[]uintptr{1012}, true, ann("12")},
		"no announcement":                 {[]uintptr{1012}, true, []string{"A=1"}},
		"announced twice":                 {[]uintptr{1012}, true, append(ann("1012"), coderhost.CredFDEnv+"=1012")},
		"replaced, same parent":           {[]uintptr{1012}, false, ann("1012")},
		"replaced in place":               {[]uintptr{55}, false, ann("55")},
		"not the pipe in the new parent":  {[]uintptr{1013}, true, ann("1013")},
		"own number under the new parent": {[]uintptr{12}, true, ann("12")},
	} {
		if err := placedPipeKept(12, c.list, c.reparented, c.env, isPipe); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
