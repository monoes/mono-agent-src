package chat

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

const unsandboxedMark = "without a sandbox"

// warningNotices returns the turn's agent.sandbox notices that are warnings.
func warningNotices(j journaledTurn) []chatevents.NoticePayload {
	var out []chatevents.NoticePayload
	for _, n := range j.byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(n.Payload, &p)
		if p.Code == noticeAgentSandbox && p.Severity == chatevents.SeverityWarning {
			out = append(out, p)
		}
	}
	return out
}

// processStderr is what f writes to the process's stderr, where monomind.Exec
// says a turn has no sandbox.
func processStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	drained := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- b
	}()
	os.Stderr = w
	defer func() { os.Stderr = prev }()
	f()
	w.Close()
	return string(<-drained)
}

// Every turn that ran without a sandbox says so, on stderr and in the journal;
// a turn that had one (or claude's scoped limits) does not.
func TestEveryUnsandboxedTurnWarns(t *testing.T) {
	for _, tc := range []struct {
		name, version, runtime, setting, startExtra string
		advertise, want                             bool
		caps                                        []string
	}{
		{name: "runtime without a sandbox", version: "9.0.0", advertise: true, runtime: "copilot", setting: "run", want: true},
		{name: "awaiting monomind", version: "2.18.5", runtime: "qwen", setting: "run", want: true},
		{name: "old monomind", version: "2.10.0", runtime: "codex", setting: "run", want: true},
		{name: "monomind reports off", version: "9.0.0", advertise: true, runtime: "codex", setting: "run", startExtra: `,"sandbox":"off"`, want: true},
		{name: "ignored strictest", version: "2.24.5", advertise: true, runtime: "antigravity", setting: "strictest", want: true},
		{name: "sandboxed", version: "9.0.0", advertise: true, runtime: "codex", setting: "run", startExtra: `,"sandbox":"workspace-write"`},
		{name: "claude scoped", version: "9.0.0", advertise: true, runtime: "claude", setting: "run"},
		{name: "restricted fallback", version: "2.24.5", advertise: true, runtime: "antigravity", setting: "strictest",
			startExtra: `,"sandbox_applied":"restricted"`, caps: []string{monomind.CapAgentExecSandboxFallback}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := newChatCLITestDB(t)
			t.Setenv(monomind.SandboxFallbackEnv, tc.setting)
			conv, err := openTestChatStore(t, dbPath).CreateConversation("default", "agent", "general", tc.runtime, "", "")
			if err != nil {
				t.Fatal(err)
			}
			bin, _ := writeSandboxMonomind(t, tc.version, tc.advertise, sandboxTranscript(tc.startExtra), tc.caps...)
			t.Setenv(monomind.EnvOverride, bin)
			for _, turn := range []string{"turn-1", "turn-2"} {
				var out string
				errOut := processStderr(t, func() {
					out, _ = chatWithStderr(t, dbPath, "--conversation", conv.ID, "--turn", turn, "--", "hi")
				})
				notices := warningNotices(parseJournaledTurn(t, out))
				if (len(notices) == 1) != tc.want || len(notices) > 1 {
					t.Errorf("%s: warning notices = %+v, want one: %v", turn, notices, tc.want)
				}
				if got := strings.Count(errOut, unsandboxedMark); (got == 1) != tc.want || got > 1 {
					t.Errorf("%s: stderr warned %d times, want one: %v; %q", turn, got, tc.want, errOut)
				}
			}
		})
	}
}

// A plain `chat` turn warns on stderr and keeps stdout as protocol JSON.
func TestUnsandboxedPlainTurnWarns(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	t.Setenv(monomind.SandboxFallbackEnv, "run")
	bin, _ := writeSandboxMonomind(t, "9.0.0", true, sandboxTranscript(""))
	t.Setenv(monomind.EnvOverride, bin)
	var out string
	errOut := processStderr(t, func() {
		out, _ = chatWithStderr(t, dbPath, "--runtime", "copilot", "--no-history", "--", "hi")
	})
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !json.Valid([]byte(l)) {
			t.Errorf("stdout line is not JSON: %q", l)
		}
	}
	if strings.Count(errOut, unsandboxedMark) != 1 {
		t.Errorf("stderr = %q", errOut)
	}
}

// A research worker that starts without read access or a sandbox says so once
// per run; a confined one, and the same worker resuming or changing leases,
// does not.
func TestUnconfinedResearchWorkerWarnsOncePerRun(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversation("default", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateTurn(conv.ID, "default", "turn-1", "i", "hi"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	j := newTurnJournal(store, "default", conv.ID, "turn-1", "claude", &out)
	status := func(from, to, confinement string) chatevents.AgentStatusPayload {
		return chatevents.AgentStatusPayload{AgentID: "w1", From: from, To: to, Confinement: confinement}
	}
	errOut := processStderr(t, func() {
		for _, p := range []chatevents.AgentStatusPayload{
			status(chatevents.AgentQueued, chatevents.AgentStarting, chatevents.ConfinementWriteLease),
			status(chatevents.AgentStarting, chatevents.AgentWorking, chatevents.ConfinementWriteLease),
			status(chatevents.AgentWorking, chatevents.AgentWorking, chatevents.ConfinementWriteLease),
			status(chatevents.AgentWaitingUser, chatevents.AgentWorking, chatevents.ConfinementWriteLease),
			status(chatevents.AgentStarting, chatevents.AgentWorking, chatevents.ConfinementAccessRead),
			status(chatevents.AgentStarting, chatevents.AgentWorking, chatevents.ConfinementSandbox),
			status(chatevents.AgentStarting, chatevents.AgentWorking, ""),
		} {
			j.Emit(chatevents.EventAgentStatus, p)
		}
	})
	if got := strings.Count(errOut, unsandboxedMark); got != 1 || !strings.Contains(errOut, "w1") {
		t.Errorf("%d warnings, want one naming the worker: %q", got, errOut)
	}
	if got := len(strings.Split(strings.TrimSpace(out.String()), "\n")); got != 7 {
		t.Errorf("%d events journaled, want all 7", got)
	}
}
