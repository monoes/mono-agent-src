package procchain

import "testing"

// table is a fake process table for the Windows decisions; a pid that is not in it cannot be opened.
type table map[int]procInfo

func (t table) read(pid int) (procInfo, error) {
	i, ok := t[pid]
	if !ok {
		return procInfo{}, errUnreadable
	}
	return i, nil
}

func TestParentOfBelievesOnlyAParentThatExistedWhenTheChildWasCreated(t *testing.T) {
	tab := table{
		10: {parent: 7, created: 500},
		7:  {parent: 1, created: 100},
	}
	if p, ok := parentOf(tab.read, 10); !ok || p != 7 {
		t.Fatalf("a real parent: %d %v", p, ok)
	}
	// the real parent exited and its number went to a process that started later
	tab[7] = procInfo{parent: 1, created: 900}
	if p, ok := parentOf(tab.read, 10); ok {
		t.Fatalf("a recycled parent number was believed: %d", p)
	}
}

func TestParentOfFailsClosed(t *testing.T) {
	for name, tab := range map[string]table{
		"the parent cannot be opened":   {10: {parent: 7, created: 500}},
		"the child cannot be opened":    {7: {parent: 1, created: 100}},
		"the child has no creation":     {10: {parent: 7}, 7: {created: 100}},
		"the parent has no creation":    {10: {parent: 7, created: 500}, 7: {}},
		"the child names no parent":     {10: {created: 500}},
		"the child names itself":        {10: {parent: 10, created: 500}},
		"the child names a negative id": {10: {parent: -3, created: 500}},
	} {
		if p, ok := parentOf(tab.read, 10); ok {
			t.Errorf("%s: believed %d", name, p)
		}
	}
}

func TestParentOfRefusesAChildThatChangedUnderItsNumber(t *testing.T) {
	reads := 0
	tab := table{10: {parent: 7, created: 500}, 7: {parent: 1, created: 100}}
	read := func(pid int) (procInfo, error) {
		if pid == 10 {
			reads++
			if reads == 2 { // the number now names another process
				return procInfo{parent: 7, created: 777}, nil
			}
		}
		return tab.read(pid)
	}
	if p, ok := parentOf(read, 10); ok {
		t.Fatalf("a process that changed between two reads was believed: %d", p)
	}
}

func TestStampOfNamesOnlyALiveReadableProcess(t *testing.T) {
	tab := table{
		1: {created: 42},
		2: {created: 42, exited: true},
		3: {created: 0},
	}
	if got := stampOf(tab.read, 1); got != "start:42" {
		t.Errorf("live: %q", got)
	}
	for _, pid := range []int{2, 3, 4} {
		if got := stampOf(tab.read, pid); got != "" {
			t.Errorf("pid %d: %q, want none", pid, got)
		}
	}
	if stampOf(tab.read, 1) == stampOf(table{1: {created: 43}}.read, 1) {
		t.Error("two creation times gave one stamp")
	}
}
