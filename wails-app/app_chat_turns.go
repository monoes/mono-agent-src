package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// ─── Turn process ───────────────────────────────────────────────────────────

// chatAdmission is the turn process's first stdout line.
type chatAdmission struct {
	Admitted bool          `json:"admitted"`
	Existed  bool          `json:"existed"`
	Turn     ai.TurnRecord `json:"turn"`
}

// chatTurnArgs builds the turn process's argv. --json makes a turn that
// fails before admission print {"error","code"} as its only stdout line.
// A coder conversation's turn is started with tools=false: the CLI takes
// its mode and folder from the conversation and refuses --tools for it.
func chatTurnArgs(profileID, conversationID, turnID, instanceID, message string, tools, allowRuns bool) []string {
	args := []string{"--profile", profileID, "--json", "chat", "--conversation", conversationID, "--turn", turnID, "--instance", instanceID}
	if tools {
		toolsFlag := "monoagent"
		if allowRuns {
			toolsFlag = "monoagent,runs"
		}
		args = append(args, "--tools", toolsFlag)
	}
	return append(args, "--", message)
}

// turnOutput is what a turn process's pipes produced: its JSON stdout lines
// in order, then (after close) its stderr, exit error and exit code.
type turnOutput struct {
	lines      chan []byte
	stderrDone chan struct{}
	stderr     *strings.Builder
}

func readTurnOutput(proc chatProcess) *turnOutput {
	o := &turnOutput{lines: make(chan []byte, 256), stderrDone: make(chan struct{}), stderr: &strings.Builder{}}
	go func() {
		defer close(o.stderrDone)
		_, _ = io.Copy(o.stderr, proc.Stderr())
	}()
	go func() {
		defer close(o.lines)
		sc := bufio.NewScanner(proc.Stdout())
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			o.lines <- append([]byte(nil), line...)
		}
	}()
	return o
}

// exited waits for the process after stdout closed, then for stderr to be
// fully drained (Wait is what force-closes a pipe a straggling grandchild
// still holds, so stderr is joined after it, not before).
func (o *turnOutput) exited(proc chatProcess) (waitErr error, stderr string) {
	waitErr = proc.Wait()
	<-o.stderrDone
	return waitErr, o.stderr.String()
}

// startTurn launches the turn process and waits for its admission line.
// It returns the StartChatTurn response; on success the turn keeps
// streaming in the background.
func (sup *chatSupervisor) startTurn(h *chatTurnHandle, message string, tools, allowRuns bool) (string, error) {
	cliBin, err := sup.findCLI()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancel = cancel
	h.mu.Unlock()
	proc, err := sup.launcher(ctx, cliBin, chatTurnArgs(h.profileID, h.conversationID, h.turnID, sup.instanceID, message, tools, allowRuns))
	if err != nil {
		cancel()
		return "", err
	}
	h.mu.Lock()
	h.proc = proc
	stopped := h.stopRequested
	h.mu.Unlock()
	if stopped {
		proc.Kill()
	}

	out := readTurnOutput(proc)
	var adm chatAdmission
	select {
	case line, ok := <-out.lines:
		var refusal chatRefusal
		if ok {
			err = json.Unmarshal(line, &adm)
			_ = json.Unmarshal(line, &refusal)
		}
		if !ok || err != nil || adm.Turn.ID == "" {
			proc.Kill()
			for range out.lines {
			}
			waitErr, stderr := out.exited(proc)
			cancel()
			if h.isStopRequested() {
				// Stopped while starting: the process may have registered
				// the turn before it died.
				sup.finishStoppedStart(h)
			}
			return "", admissionError(waitErr, stderr, refusal)
		}
	case <-time.After(chatAdmissionTimeout):
		proc.Kill()
		go func() {
			for range out.lines {
			}
			_, _ = out.exited(proc)
		}()
		cancel()
		return "", fmt.Errorf("monoagentcli did not start the turn within %s", chatAdmissionTimeout)
	}

	if adm.Existed {
		// This exact turn id already ran (e.g. a Start retried after a
		// restart): the process reports it and exits without running it.
		go func() {
			for range out.lines {
			}
			_, _ = out.exited(proc)
			cancel()
		}()
		sup.release(h.conversationID, h.turnID)
		b, _ := json.Marshal(map[string]any{"ok": true, "turnId": h.turnID, "status": adm.Turn.Status})
		return string(b), nil
	}

	sup.running.Add(1)
	go func() {
		defer sup.running.Done()
		defer cancel()
		sup.relayTurn(h, proc, out)
	}()
	return fmt.Sprintf(`{"ok":true,"turnId":%q,"status":"active"}`, h.turnID), nil
}

// chatRefusal is the {"error","code"} line a turn process prints (under
// --json) when it fails before admitting its turn.
type chatRefusal struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// codedError is an error the CLI classified: its code travels to the
// frontend next to the message (e.g. agent_not_setup).
type codedError struct {
	msg  string
	code string
}

func (e *codedError) Error() string { return e.msg }

// admissionError explains a turn process that exited without admitting
// its turn, keeping the CLI's error code when it printed one. An older
// monoagentcli that predates --conversation is called out, since it would
// otherwise read like any other failure.
func admissionError(waitErr error, stderr string, refusal chatRefusal) error {
	if strings.Contains(stderr, "unknown flag") {
		return fmt.Errorf("the installed monoagentcli does not recognize a flag this app requires (%s); update monoagentcli to match this app's version", lastLine(stderr))
	}
	msg := lastLine(stderr)
	if msg == "" {
		msg = refusal.Error // the process was killed before its stderr
	}
	if msg != "" {
		if refusal.Code != "" {
			return &codedError{msg: msg, code: refusal.Code}
		}
		return errors.New(msg)
	}
	if waitErr != nil {
		return fmt.Errorf("monoagentcli exited before starting the turn: %w", waitErr)
	}
	return errors.New("monoagentcli exited before starting the turn")
}

// relayTurn emits each committed event the turn process prints, in order,
// and finishes the turn itself if the process ends without having done so.
func (sup *chatSupervisor) relayTurn(h *chatTurnHandle, proc chatProcess, out *turnOutput) {
	finished := false
	for line := range out.lines {
		var rec chatevents.Record
		if json.Unmarshal(line, &rec) != nil || rec.Type == "" {
			continue
		}
		if rec.Type == chatevents.EventTurnFinished {
			finished = true
		}
		if sup.emit != nil {
			sup.emit(rec.Event())
		}
	}
	waitErr, stderr := out.exited(proc)
	if !finished {
		sup.finishOrphanedTurn(h, waitErr, stderr)
	}
	sup.release(h.conversationID, h.turnID)
}

type chatFinishResult struct {
	Finalized bool               `json:"finalized"`
	Event     *chatevents.Record `json:"event"`
}

// finishOrphanedTurn records the end of a turn whose process exited without
// its turn.finished — killed by Stop, crashed, or cut off — through
// `chat history finish`, and emits the committed event. If that fails too,
// the UI still gets a live-only turn.finished (seq MaxSafeSeq so the
// frontend never drops it as stale, historySaved false) instead of hanging.
func (sup *chatSupervisor) finishOrphanedTurn(h *chatTurnHandle, waitErr error, stderr string) {
	status, reason := chatevents.ComputeTurnStatus(h.isStopRequested(), nil)
	var exitCode *int
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ExitCode() >= 0 {
		v := ee.ExitCode()
		exitCode = &v
	}
	if !h.isStopRequested() {
		if waitErr != nil {
			status, reason = chatevents.StatusFailed, "monoagentcli exited: "+waitErr.Error()
			if msg := lastLine(stderr); msg != "" {
				reason += ": " + msg
			}
		} else {
			reason = "monoagentcli ended without finishing the turn"
		}
	}
	args := []string{"chat", "history", "finish", h.conversationID, h.turnID, "--status", string(status), "--reason", reason}
	if exitCode != nil {
		args = append(args, "--exit-code", strconv.Itoa(*exitCode))
	}
	var res chatFinishResult
	if err := sup.cli(h.profileID, &res, args...); err != nil {
		live, buildErr := chatevents.New(h.profileID, h.conversationID, h.turnID, chatevents.MaxSafeSeq, time.Now(), chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{
			Status: status, Reason: reason, ExitCode: exitCode, HistorySaved: false,
		})
		if buildErr == nil && sup.emit != nil {
			sup.emit(live)
		}
		return
	}
	if res.Finalized && res.Event != nil && sup.emit != nil {
		sup.emit(res.Event.Event())
	}
}

// finishStoppedStart records a turn stopped before its process admitted it
// as cancelled, if the process got as far as creating it. Unlike
// finishOrphanedTurn it emits nothing when there is no such turn.
func (sup *chatSupervisor) finishStoppedStart(h *chatTurnHandle) {
	status, reason := chatevents.ComputeTurnStatus(true, nil)
	var res chatFinishResult
	if sup.cli(h.profileID, &res, "chat", "history", "finish", h.conversationID, h.turnID, "--status", string(status), "--reason", reason) == nil &&
		res.Finalized && res.Event != nil && sup.emit != nil {
		sup.emit(res.Event.Event())
	}
}
