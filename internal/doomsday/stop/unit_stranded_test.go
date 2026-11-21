package stop

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func strandedRec() UnitRecord {
	h, _ := json.Marshal(pgHandle{Pgid: 500, Sid: 400, CreatorPID: 9, CreatorStamp: "c"})
	return UnitRecord{Kind: KindPGroup, Generation: 3, AnchorPID: 500, AnchorStamp: "a", Handle: string(h)}
}

// A group anchor started by a host that is not the running one cannot take a turn from this host (a process cannot join a
// group across sessions): the unit is stranded, and the reason names the host.
func TestStrandedUnitReason(t *testing.T) {
	ppid := func(parent int) func(int) (int, error) { return func(int) (int, error) { return parent, nil } }
	gone := func(int) (int, error) { return 0, errors.New("no such process") }
	cases := []struct {
		name     string
		rec      func() UnitRecord
		host     int
		ppidOf   func(int) (int, error)
		stranded bool
	}{
		{"anchor still a child of the running host", strandedRec, 700, ppid(700), false},
		{"anchor orphaned: its host ended", strandedRec, 700, ppid(1), true},
		{"anchor of another live host", strandedRec, 700, ppid(650), true},
		{"no host is running", strandedRec, 0, ppid(1), true},
		{"the host's pid is not known: no claim", strandedRec, -1, ppid(1), false},
		{"anchor gone: not this check's case", strandedRec, 700, gone, false},
		{"not a process-group unit", func() UnitRecord { r := strandedRec(); r.Kind = "cgroup2"; return r }, 700, ppid(1), false},
	}
	for _, c := range cases {
		got := strandedUnitReason(c.rec(), c.host, c.ppidOf)
		if (got != "") != c.stranded {
			t.Errorf("%s: reason %q, want stranded=%v", c.name, got, c.stranded)
		}
		if c.stranded && !strings.Contains(got, "host") {
			t.Errorf("%s: the reason does not name the host: %q", c.name, got)
		}
	}
}
