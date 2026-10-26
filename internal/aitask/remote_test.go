package aitask

import "testing"

// The daemon API cannot place its caller in the task's process tree, so Remote checks the
// token's hash only; a local service still refuses a caller outside the holder's tree.
func TestRemoteServiceSkipsOnlyTheProcessTreeCheck(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Store.Insert(Task{ID: "at-1", Kind: "generic", ConversationID: "c1", Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SetToken("at-1", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SetHolder("at-1", 4242); err != nil {
		t.Fatal(err)
	}
	was := ProcessWithin
	ProcessWithin = func(int) bool { return false }
	defer func() { ProcessWithin = was }()

	if svc.Store.CheckToken("at-1", "tok") {
		t.Error("a caller outside the holder's tree passed the local check")
	}
	if !svc.Store.CheckTokenRemote("at-1", "tok") {
		t.Error("the remote check refused the right token")
	}
	for _, bad := range []string{"", "tok2"} {
		if svc.Store.CheckTokenRemote("at-1", bad) {
			t.Errorf("the remote check accepted token %q", bad)
		}
	}
	if svc.Store.CheckTokenRemote("at-other", "tok") {
		t.Error("the remote check accepted a token for a task that has none")
	}
	if _, err := svc.Complete("at-1", "tok", []byte(`{"summary":"x"}`)); err == nil {
		t.Error("a local service completed from outside the holder's tree")
	}
	svc.Remote = true
	if _, err := svc.Complete("at-1", "tok", []byte(`{"summary":"x"}`)); err != nil {
		t.Errorf("a remote service refused the holder's token: %v", err)
	}
}
