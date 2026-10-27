package procchain

import (
	"os"
	"testing"
)

func TestInChain(t *testing.T) {
	self := os.Getpid()
	stamp := Stamp(self)
	if Supported && stamp == "" {
		t.Fatal("no stamp for this process")
	}
	cases := []struct {
		name  string
		chain []int
		pid   int
		stamp string
		want  bool
	}{
		{"the process itself", []int{self, 9}, self, stamp, true},
		{"an ancestor", []int{5, self, 9}, self, stamp, true},
		{"not in the chain", []int{5, 6}, self, stamp, false},
		{"empty chain", nil, self, stamp, false},
		{"pid 1 never counts", []int{1, 2}, 1, "", false},
		{"pid 0 never counts", []int{0}, 0, "", false},
	}
	for _, c := range cases {
		if got := InChain(c.chain, c.pid, c.stamp); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if Supported && InChain([]int{self}, self, "start:0") {
		t.Error("a pid held by a process with another start stamp counted (pid reuse)")
	}
}

// An unreadable chain fails closed: only what was read counts.
func TestDescendsFromFailsClosedOnAnUnreadableChain(t *testing.T) {
	orig := Chain
	t.Cleanup(func() { Chain = orig })
	Chain = func() ([]int, bool) { return []int{10}, false }
	if DescendsFrom(77, "") {
		t.Error("an unreadable chain that does not show the process counted")
	}
	Chain = func() ([]int, bool) { return []int{10, 77}, false }
	if !DescendsFrom(77, "") {
		t.Error("a partial chain that shows the process was refused")
	}
}

func TestRealChainStartsAtThisProcess(t *testing.T) {
	if !Supported {
		t.Skip("ancestry is read on Linux only")
	}
	chain, _ := Chain()
	if len(chain) == 0 || chain[0] != os.Getpid() {
		t.Fatalf("chain %v", chain)
	}
	if !DescendsFrom(os.Getpid(), Stamp(os.Getpid())) {
		t.Error("this process is not its own descendant")
	}
}
