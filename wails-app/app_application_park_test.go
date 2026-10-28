//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestGetApplicationTab(t *testing.T) {
	a, log := aitaskApp(t, `echo '{"applicationId":"app-1","tab":{"applicationId":"app-1","taskId":"t1","tabId":7,"origin":"https://x.test","state":"parked","updatedAt":"u"},"submitObservedAt":"2026-10-22T10:00:00Z"}'`+"\n")
	got, err := a.GetApplicationTab("app-1")
	if err != nil || got.Tab == nil || got.Tab.TabID != 7 || got.Tab.State != "parked" || got.SubmitObservedAt == "" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != "--profile work --json application fill tab app-1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestGetApplicationTabNull(t *testing.T) {
	a, _ := aitaskApp(t, `echo '{"applicationId":"app-1","tab":null,"submitObservedAt":""}'`+"\n")
	got, err := a.GetApplicationTab("app-1")
	if err != nil || got.Tab != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// ConfirmApplicationSubmitted must go through `application send`, the only
// route to applied, and nothing else.
func TestConfirmApplicationSubmittedIsApplicationSend(t *testing.T) {
	a, log := aitaskApp(t, "echo '{}'\n")
	if err := a.ConfirmApplicationSubmitted("app-1"); err != nil {
		t.Fatal(err)
	}
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != "--profile work --json application send app-1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestResumeApplicationFillReadsTheTaskBack(t *testing.T) {
	a, log := aitaskApp(t, `case "$*" in
*"fill resume"*) echo '{"applicationId":"app-1","taskId":"t2","tabId":7,"tabState":"active"}';;
*) echo '{"taskId":"t2","conversationId":"c2","status":"running"}';;
esac
`)
	got, err := a.ResumeApplicationFill("app-1")
	if err != nil || got.TaskID != "t2" || got.ConversationID != "c2" {
		t.Fatalf("got %+v, %v", got, err)
	}
	want := "--profile work --json application fill resume app-1\n--profile work --json aitask status t2"
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != want {
		t.Fatalf("calls = %v", calls)
	}
}

func TestResumeApplicationFillCoderDisabled(t *testing.T) {
	a, _ := aitaskApp(t, `echo '{"error":"off","code":"coder_disabled","fallback":"oneshot"}'; exit 3`+"\n")
	got, err := a.ResumeApplicationFill("app-1")
	if err != nil || got.Fallback != "oneshot" || got.Reason != "off" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSweepApplicationTabs(t *testing.T) {
	a, _ := aitaskApp(t, `echo '{"closed":["a"],"lost":null}'`+"\n")
	got, err := a.SweepApplicationTabs()
	if err != nil || len(got.Closed) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestStartApplicationQueueReturnsAtOnceAndRunsTheCLI(t *testing.T) {
	a, log := aitaskApp(t, "sleep 1\n")
	a.ctx = nil // no Wails runtime in tests: the exit event is skipped
	a.runningCmds = map[string]*exec.Cmd{}
	start := time.Now()
	if err := a.StartApplicationQueue([]string{"a1", "a2"}, 2); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("StartApplicationQueue waited for the queue")
	}
	if err := a.StartApplicationQueue([]string{"a3"}, 0); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second queue: %v", err)
	}
	var b []byte
	for i := 0; i < 50 && len(b) == 0; i++ { // the script logs its argv as it starts
		time.Sleep(20 * time.Millisecond)
		b, _ = os.ReadFile(log)
	}
	a.stopRunningCmds()
	if !strings.Contains(string(b), "--profile work --json application fill queue a1 a2 --max-parked 2") {
		t.Fatalf("CLI got %q", b)
	}
}

func TestStartApplicationQueueRefusesBadInput(t *testing.T) {
	a, log := aitaskApp(t, "sleep 1\n")
	for _, tc := range []struct {
		ids []string
		max int
	}{{nil, 0}, {[]string{"a"}, 11}, {[]string{"a"}, -1}} {
		if err := a.StartApplicationQueue(tc.ids, tc.max); err == nil {
			t.Fatalf("accepted %v max %d", tc.ids, tc.max)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran for refused input")
	}
}

func TestQueueFailuresNamesTheFillThatCouldNotStart(t *testing.T) {
	if got := queueFailures([]byte(`{"max":3,"outcomes":[{"applicationId":"a","result":"parked","taskId":"t"}]}`)); got != "" {
		t.Fatalf("clean queue reported %q", got)
	}
	got := queueFailures([]byte(`{"max":3,"outcomes":[{"applicationId":"a","result":"failed","error":"no posting URL"}]}`))
	if !strings.Contains(got, "no posting URL") {
		t.Fatalf("got %q", got)
	}
	if queueFailures([]byte("not json")) != "" {
		t.Fatal("garbage reported as a failure")
	}
}
