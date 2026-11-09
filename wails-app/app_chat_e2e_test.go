//go:build !windows

package main

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// TestChatEndToEndWithTheRealCLI drives the bindings against a freshly built
// monoagentcli and a fake monomind, in a throwaway HOME: the app/CLI
// contract (argv, admission line, event records, history JSON) for real.
// It builds the CLI, so it only runs with MONOAGENT_CHAT_E2E=1.
func TestChatEndToEndWithTheRealCLI(t *testing.T) {
	if os.Getenv("MONOAGENT_CHAT_E2E") != "1" {
		t.Skip("set MONOAGENT_CHAT_E2E=1 to build monoagentcli and run the end-to-end chat test")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "monoagentcli")
	build := exec.Command("go", "build", "-tags", "devaccount", "-o", cli, "./cmd/monoagentcli")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building monoagentcli: %v\n%s", err, out)
	}
	t.Setenv("MONOAGENT_DEV_ENFORCE_FROM", "2999-01-01T00:00:00Z")
	monomind := filepath.Join(dir, "monomind")
	os.WriteFile(monomind, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then
  echo '{"v":1,"version":"2.10.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  echo '{"v":1,"type":"start","runtime":"fake","cwd":"/app","pid":1}'
  echo '{"v":1,"type":"session","session_id":"th_e2e"}'
  echo '{"v":1,"type":"assistant","text":"hello from the fake runtime"}'
  if [ -n "$FAKE_HANG" ]; then sleep 30; fi
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn"}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
exit 2
`), 0o755)
	testhome.SetHome(t, filepath.Join(dir, "home"))
	t.Setenv("MONOMIND_BIN", monomind)

	emitter := &collectingEmitter{}
	sup := newChatSupervisor(defaultChatProcessLauncher, emitter.emit, func() (string, error) { return cli, nil })
	t.Cleanup(sup.stopAll)
	a := &App{chatSup: sup}
	a.selectForTest("default")

	var conv struct {
		ID        string `json:"id"`
		Backend   string `json:"backend"`
		RuntimeID string `json:"runtimeId"`
	}
	out := a.CreateChatConversation("general", "fake", "", "")
	if err := json.Unmarshal([]byte(out), &conv); err != nil || conv.Backend != "agent" || conv.RuntimeID != "fake" {
		t.Fatalf("CreateChatConversation = %s", out)
	}
	if errs := sup.reconcileOrphanedTurns(); len(errs) != 0 {
		t.Fatalf("reconcile: %v", errs)
	}

	if r := decodeStart(t, a.StartChatTurn(conv.ID, "turn-1", "history", false, false, nil)); !r.OK || r.Status != "active" {
		t.Fatalf("Start = %+v", r)
	}
	fin := waitForType(t, emitter, chatevents.EventTurnFinished, 30*time.Second)
	var p chatevents.TurnFinishedPayload
	jsonUnmarshalPayload(fin[0], &p)
	if p.Status != chatevents.StatusCompleted || !p.HistorySaved {
		t.Fatalf("turn.finished = %+v", p)
	}
	waitReleased(t, sup, conv.ID, "turn-1")
	live := emitter.snapshot()

	var events struct {
		Items            []chatevents.Event `json:"items"`
		LastCommittedSeq int64              `json:"lastCommittedSeq"`
	}
	out = a.GetChatEvents(conv.ID, "turn-1", 0, 200)
	if err := json.Unmarshal([]byte(out), &events); err != nil {
		t.Fatalf("GetChatEvents = %s", out)
	}
	if len(events.Items) != len(live) || events.LastCommittedSeq != live[len(live)-1].Seq {
		t.Fatalf("history (%d events, last %d) != live stream (%d events): %s", len(events.Items), events.LastCommittedSeq, len(live), out)
	}
	for i := range live {
		if live[i].Seq != events.Items[i].Seq || live[i].Type != events.Items[i].Type || string(live[i].Payload) != string(events.Items[i].Payload) {
			t.Errorf("event %d: live %+v, stored %+v", i, live[i], events.Items[i])
		}
	}

	// A long turn, stopped: the kill leaves the finish to the app.
	t.Setenv("FAKE_HANG", "1")
	if r := decodeStart(t, a.StartChatTurn(conv.ID, "turn-2", "wait", false, false, nil)); !r.OK {
		t.Fatalf("Start turn-2 = %+v", r)
	}
	if out := a.DeleteChatConversation(conv.ID); !strings.Contains(out, "active turn") {
		t.Errorf("Delete during a turn = %s, want a refusal", out)
	}
	time.Sleep(300 * time.Millisecond)
	if out := a.StopChatTurn(conv.ID, "turn-2"); out != `{"ok":true}` {
		t.Fatalf("Stop = %s", out)
	}
	waitReleased(t, sup, conv.ID, "turn-2")
	var turns struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	out = a.GetChatTurns(conv.ID, "", 10)
	json.Unmarshal([]byte(out), &turns)
	if len(turns.Items) != 2 || turns.Items[0].Status != "cancelled" && turns.Items[1].Status != "cancelled" {
		t.Errorf("turns after Stop = %s", out)
	}

	if out := a.DeleteChatConversation(conv.ID); out != `{"ok":true}` {
		t.Fatalf("Delete = %s", out)
	}
	if out := a.ListChatConversations("", 10); out != `{"items":[],"nextCursor":""}` {
		t.Errorf("List after delete = %s", out)
	}
}
