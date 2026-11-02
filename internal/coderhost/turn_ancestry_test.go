package coderhost

import (
	"os"
	"testing"
)

func TestCallerInATurnSeesAProcessInsideARunningTurn(t *testing.T) {
	if !ancestrySupported {
		t.Skip("process ancestry cannot be read here")
	}
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	orig := callerChain
	t.Cleanup(func() { callerChain = orig })

	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 7, 3}, true }
	if in, err := st.CallerInATurn(); err != nil || in {
		t.Fatalf("no turn runs: %v %v", in, err)
	}
	pid := os.Getpid() + 100000
	if _, err := st.db.Exec(`UPDATE coder_sessions SET turn_pid = ?, turn_stamp = '' WHERE conversation_id = ?`, pid, m.ConversationID); err != nil {
		t.Fatal(err)
	}
	if in, _ := st.CallerInATurn(); in {
		t.Fatal("a turn elsewhere in the tree is not the caller's")
	}
	callerChain = func() ([]int, bool) { return []int{os.Getpid(), 7, pid, 1}, true }
	if in, err := st.CallerInATurn(); err != nil || !in {
		t.Fatalf("a descendant of the turn's process must be seen: %v %v", in, err)
	}
	callerChain = func() ([]int, bool) { return []int{os.Getpid()}, false }
	if in, _ := st.CallerInATurn(); in {
		t.Fatal("an unreadable chain that does not show the turn is a no")
	}
}
