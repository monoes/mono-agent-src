//go:build unix

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// A v2 task token (mat2-) reaches a bound worker only through the credential frame, into an in-process
// stash (coderhost.StashCredFrame), and no environment variable carries it. The question these tests
// settle: which layer of requireOperator refuses a process that holds only that stash and nothing
// else (a clean operator environment, no markers, no token variable).
//
// The finding: the token-variable layer cannot (nothing in the environment), the marker check cannot
// (the stash is not a marker), and the stash is read by nobody but the process itself (the chat
// takes it once, TakeMissionToken, and puts it in MONOAGENT_TASK_BINDING for its own children). What
// refuses the one process that holds the stash is the coder-turn ancestry layer: the launcher records
// that process as the session's turn (RecordTurnProcess, coder_host_exec.go) BEFORE it delivers the
// frame (deliver), a mission child's turn included, since a child's session is a coder_sessions row.
// The descriptor and the stash are not inherited: openInherited sets close-on-exec, the stash is
// package memory, and turnEnv strips the frame marker from what the turn starts.

func stashAV2TaskToken(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := procbind.CredFrame{TaskToken: []byte("mat2-" + strings.Repeat("b", 40))}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	w.Close()
	fd, err := syscall.Dup(int(r.Fd())) // StashCredFrame closes the descriptor it is given
	if err != nil {
		t.Fatal(err)
	}
	coderhost.TakeMissionToken() // nothing stale
	t.Setenv(coderhost.CredFDEnv, strconv.Itoa(fd))
	if err := coderhost.StashCredFrame(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { coderhost.TakeMissionToken() })
}

// The environment carries nothing of it: the token-variable layer has nothing to read.
func TestAV2TaskTokenInTheStashLeavesNoTraceInTheEnvironment(t *testing.T) {
	sendEnv(t)
	stashAV2TaskToken(t)
	for _, kv := range os.Environ() {
		if strings.Contains(kv, "mat2-") {
			t.Fatalf("the task token is in the environment: %.20s...", kv)
		}
	}
	if os.Getenv(coderhost.CredFDEnv) != "" {
		t.Error("the frame marker stayed in the environment")
	}
}

// The negative control: the stash by itself is not read by the gate. A process that holds it and is
// not recorded as a turn passes, so the layer below is the one that matters. (No such process exists:
// the only holder is the turn's own leader, and a gated verb is a separate process that has no stash.)
func TestAV2TaskTokenInTheStashIsNotItselfSeenByTheGate(t *testing.T) {
	for _, r := range requireOperatorRows() {
		t.Run(r.name, func(t *testing.T) {
			cfg := sendEnv(t)
			stashAV2TaskToken(t)
			if err := r.run(t, cfg); isOperatorOnly(err) {
				t.Fatalf("a layer reads the stash after all (update the finding): %v", err)
			}
		})
	}
}

// The process that holds the stash is the turn's leader, recorded as the session's turn before the
// frame is delivered: the ancestry layer refuses it on every consumer, with the turn wording.
func TestTheProcessHoldingAV2TaskTokenIsRefusedByTheTurnAncestry(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for _, r := range requireOperatorRows() {
		t.Run(r.name, func(t *testing.T) {
			cfg := sendEnv(t)
			markThisProcessAsACoderTurn(t, cfg)
			stashAV2TaskToken(t)
			err := r.run(t, cfg)
			requireOperatorOnly(t, err)
			for _, want := range []string{"coder session's turn", "only the operator can ", "run it in your own terminal"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("not refused by the turn ancestry (missing %q): %v", want, err)
				}
			}
		})
	}
}
