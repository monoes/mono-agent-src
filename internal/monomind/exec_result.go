package monomind

import (
	"fmt"
	"strings"
)

// TurnResult is the terminal state of one exec turn.
type TurnResult struct {
	ExitCode   int
	SessionID  string
	ResultText string
	// StopReason is the last result event's stop_reason (protocol §3.2:
	// end_turn/max_turns/tool_round_cap/cancelled/timeout), "" if no result
	// event was ever seen. max_turns/tool_round_cap mean the turn hit a
	// limit, not that it succeeded normally — callers must not display
	// those as plain success.
	StopReason string
	// InputTokens/OutputTokens/CostUSD mirror the last usage/result event's
	// reported metrics; Has* reports whether that metric was ever actually
	// present in any event this turn (see Event.HasCostUSD etc.) — false
	// means genuinely unavailable, not zero.
	InputTokens     int64
	OutputTokens    int64
	CostUSD         float64
	HasInputTokens  bool
	HasOutputTokens bool
	HasCostUSD      bool
	// ModelUsage is the last result event's per-model counters, nil when it had none (or no result came).
	ModelUsage map[string]ModelUsage
	// Sandbox is the start event's sandbox report, and SandboxStatus the
	// verdict (SandboxStatus* constants); "" when the turn asked for none.
	Sandbox       SandboxFields
	SandboxStatus string
	// SawDone reports whether a terminal `done` event was ever observed.
	// false with Err == nil means the process/stream ended (EOF, ctx
	// cancellation notwithstanding) without ever giving terminal protocol
	// evidence — callers must treat that as interrupted, not completed.
	SawDone bool
	Err     *ProtocolError

	// incremental is the start event's streams_incrementally: assistant
	// events are deltas of one reply, not whole messages.
	incremental bool
	// streamed holds the deltas since the last tool call, and resultText
	// whether a result event supplied its own text (which then wins).
	streamed   []byte
	resultText bool
	// warning is the last non-fatal error event: the cause a later nonzero
	// done reports (Claude Code's "Not logged in" arrives this way).
	warning *ProtocolError
}

// ApplyEventToResult updates res's terminal-state fields (SessionID,
// ResultText, StopReason, usage, SawDone, ExitCode, Err) from one protocol
// event, applying exactly the same precedence Exec's own loop uses below.
// It deliberately does NOT handle tool_call/tool_result bridging (§4) —
// that requires writing a reply frame to the subprocess's stdin, which only
// the process actually holding that pipe can do.
//
// Exported so a caller watching the identical event stream one process
// layer removed — a GUI supervisor reading `monoagentcli chat`'s stdout,
// which is byte-for-byte this same JSON passed through from onEvent below —
// can accumulate an equivalent TurnResult without re-deriving this
// precedence logic itself (internal/ai/chatevents cannot own this instead:
// it already imports monomind for TurnResult/Event, so monomind importing
// chatevents back would cycle).
func ApplyEventToResult(res *TurnResult, ev Event) {
	switch ev.Type {
	case EventSession:
		if ev.SessionID != "" {
			res.SessionID = ev.SessionID
		}
	case EventStart:
		res.incremental = ev.StreamsIncrementally
		if ev.Sandbox != "" || ev.SandboxUnsupported || ev.SandboxStatus != "" || ev.SandboxNote != "" {
			res.Sandbox = ev.SandboxFields
		}
		if ev.SandboxStatus != "" {
			res.SandboxStatus = ev.SandboxStatus
		}
	case EventAssistant:
		// Fallback source for ResultText: monomind's result event often has
		// no text (only assistant events do), so ResultText would otherwise
		// come back empty. An incremental runtime (start's
		// streams_incrementally) sends the reply as deltas, so they are
		// joined; otherwise each event is a whole message and the latest one
		// is the answer. A result event with its own text still wins. A
		// native subagent's text (parent_tool_use_id, monomind#387) is not
		// the agent's answer.
		if ev.Text == "" || res.resultText || ev.ParentToolUseID != "" {
			break
		}
		if res.incremental {
			res.streamed = append(res.streamed, ev.Text...)
			res.ResultText = string(res.streamed)
		} else {
			res.ResultText = ev.Text
		}
	case EventToolCall:
		// Text before a tool call is narration; the answer is the text
		// after the last tool round.
		res.streamed = res.streamed[:0]
	case EventUsage:
		// A snapshot, not a delta (protocol §3.2): overwrite, never
		// accumulate — the plan is explicit that summing without verified
		// delta semantics would double-count.
		applyUsage(res, ev)
	case EventResult:
		if ev.Text != "" {
			res.ResultText = ev.Text
			res.resultText = true
		}
		if ev.StopReason != "" {
			res.StopReason = ev.StopReason
		}
		applyUsage(res, ev)
		res.ModelUsage = ev.ModelUsage // this result's only: never an earlier one's beside its cost
		// A result explicitly marked is_error is a failure even when the
		// process later exits 0 and still sends done — e.g. the runtime
		// reported the model's answer as an error result but terminated
		// its own subprocess cleanly regardless. Only set this if nothing
		// already recorded a (fatal) error, so a fatal `error` event's own
		// code/message — richer than a bare "result reported is_error" —
		// still wins.
		if ev.IsError && res.Err == nil {
			res.Err = &ProtocolError{
				Code:    ErrRunnerError,
				Message: fmt.Sprintf("result reported is_error (stop_reason=%q)", ev.StopReason),
			}
		}
	case EventError:
		// Only a fatal error terminates the turn (protocol §3.4): a
		// non-fatal error (fatal:false) is a recoverable, mid-stream
		// hiccup — the golden fixture testdata/fixtures/bad-frame.ndjson
		// demonstrates a non-fatal error followed by a full successful
		// recovery (tool_result, assistant text, a success result, and
		// done exit_code:0), and TestFixtureExitCodesMatchContract already
		// asserts exit code 0 is correct for that fixture. Setting res.Err
		// here unconditionally would make both CLI callers (chat.go,
		// agent.go) treat that successful recovery as a hard failure and
		// discard the good ResultText. The event has already been
		// forwarded to onEvent by Exec's own loop, so callers that care
		// about non-fatal errors as they stream by still see them; only a
		// fatal error (or a later done/process exit with no success
		// signal) should surface as the turn's terminal res.Err.
		if ev.Fatal {
			msg := ev.ErrMessage
			if strings.Contains(msg, "did not initialize") {
				msg += " — set " + StartupTimeoutEnv + " (e.g. 180s), or for coder turns `coder set --startup-timeout 180s`, to give claude longer to start"
			}
			res.Err = &ProtocolError{Code: ev.Code, Message: msg, Fatal: ev.Fatal}
		} else {
			res.warning = &ProtocolError{Code: ev.Code, Message: ev.ErrMessage}
		}
	case EventDone:
		res.SawDone = true
		res.ExitCode = ev.ExitCode
		// A fatal error event carries no exit code of its own (protocol
		// §3.4: only done does); without this the turn's error would exit 0.
		if res.Err != nil && res.Err.ExitCode == 0 {
			res.Err.ExitCode = ev.ExitCode
		}
		// A nonzero protocol exit_code is a failure signal on its own,
		// independent of the OS process's own exit status — the two can
		// disagree (protocol says failure, process still exits 0). Only
		// set if nothing already recorded a more specific error.
		// A non-fatal error just before it is the likelier cause, and
		// says more than the exit code alone.
		if ev.ExitCode != 0 && res.Err == nil {
			res.Err = &ProtocolError{
				Code:     ErrRunnerError,
				Message:  fmt.Sprintf("done reported nonzero exit_code %d", ev.ExitCode),
				ExitCode: ev.ExitCode,
			}
			if w := res.warning; w != nil && w.Message != "" {
				res.Err.Code, res.Err.Message = w.Code, w.Message
			}
		}
	}
}

// applyUsage overwrites res's usage snapshot from ev's metrics, field by
// field — each field only if ev actually reported it (Has*), so a usage
// event that reports tokens but omits cost does not clobber a cost figure
// captured from an earlier event in the same turn.
func applyUsage(res *TurnResult, ev Event) {
	if ev.HasInputTokens {
		res.InputTokens = ev.InputTokens
		res.HasInputTokens = true
	}
	if ev.HasOutputTokens {
		res.OutputTokens = ev.OutputTokens
		res.HasOutputTokens = true
	}
	if ev.HasCostUSD {
		res.CostUSD = ev.CostUSD
		res.HasCostUSD = true
	}
}

// toolResultFrame is the client→monomind reply for a tool_call (§4.3).
type toolResultFrame struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result struct {
		Text string `json:"text"`
	} `json:"result"`
}
