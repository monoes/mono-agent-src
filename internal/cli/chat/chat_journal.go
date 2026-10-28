package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Journaled turns: `chat --conversation C --turn T -- <prompt>` runs one
// turn of a stored conversation and writes its history itself. The turn's
// runtime, model and --resume session come from the conversation. Stdout
// is NDJSON: first one admission line, then every event as a
// chatevents.Record, each printed only after it is committed. The desktop
// app relays those lines to its UI; it no longer writes the journal.

// chatAdmission is the first stdout line of a journaled turn. Existed with
// a terminal status means the turn already ran (a retried start) and the
// process exits without running it again.
type chatAdmission struct {
	Admitted bool          `json:"admitted"`
	Existed  bool          `json:"existed"`
	Turn     ai.TurnRecord `json:"turn"`
}

// admitJournaledTurn loads the conversation and registers the turn.
func admitJournaledTurn(store *ai.AIStore, profileID, conversationID, turnID, instanceID, prompt string) (ai.Conversation, ai.Turn, bool, error) {
	conv, err := store.GetConversation(conversationID, profileID)
	if err != nil {
		return ai.Conversation{}, ai.Turn{}, false, chatStoreErr(err)
	}
	if conv.Backend != "agent" {
		// "provider": the in-app AI provider stack is gone; its
		// conversations are history only.
		return ai.Conversation{}, ai.Turn{}, false, clicore.InvalidInput("conversation %s used a removed AI provider and is read-only; start a new chat", conversationID)
	}
	turn, existed, err := store.CreateTurn(conversationID, profileID, turnID, instanceID, prompt)
	if err != nil {
		return ai.Conversation{}, ai.Turn{}, false, chatStoreErr(err)
	}
	if turn.ConversationID != conversationID {
		return ai.Conversation{}, ai.Turn{}, false, clicore.InvalidInput("turn %s belongs to another conversation", turnID)
	}
	if existed && turn.Status == "active" {
		return ai.Conversation{}, ai.Turn{}, false, clicore.InvalidInput("turn %s is already running (in another window or process)", turnID)
	}
	return conv, turn, existed, nil
}

// noticeAgentSandbox is the notice a sandboxed turn journals when its
// runtime starts; its message is the monomind.SandboxStatus* value.
const noticeAgentSandbox = "agent.sandbox"

// noticeAgentSandboxDetail follows it when the status needs words: the
// mode a restricted turn really ran in, or why the sandbox-fallback
// setting was ignored.
const noticeAgentSandboxDetail = "agent.sandbox.detail"

// turnJournal turns one runtime's protocol events into committed chat
// events: it coalesces assistant text, bounds tool and notice text, binds
// the session, and finalizes the turn exactly once. Calls are serialized
// by mu (protocol callbacks and the coalescing timer both land here).
type turnJournal struct {
	store          *ai.AIStore
	profileID      string
	conversationID string
	turnID         string
	runtimeID      string
	out            io.Writer

	mu            sync.Mutex
	coalescer     *chatevents.TextCoalescer
	usage         monomind.TurnResult
	partSeq       int
	currentPartID string
	finished      bool

	// coder turns: the folder, the runtime's coder support (its name and
	// tool-activity fidelity), and each open native tool call by id.
	cwd          string
	coderRuntime monomind.CoderRuntime
	nativeRun    map[string]nativeCall
	// contextPeak is the context size of a claude turn the runner warned
	// about; runCoderTurn compacts the session instead of showing the warning.
	contextPeak int64
	// subagents journals the agent's native subagents (monomind#387).
	subagents dynorg.Subagents
	// backgroundPids are what the done event said the turn left running;
	// the notice is written at finish, for those still alive then (#294).
	backgroundPids []int
	// marker is the exec-tree marker of this turn's own process tree, learned by the host from the
	// runtime's process at the start of the turn (see learnTurnMarker); "" when it could not be read.
	markerMu   sync.Mutex
	marker     string
	launcher   *pinnedLauncher // the process monomind.Exec reported it started (OnStart), pinned
	stopMarker context.CancelFunc
	markerDone chan struct{}
}

// lockedEmitter journals through a turnJournal whose mu the caller holds.
type lockedEmitter struct{ j *turnJournal }

func (e lockedEmitter) Emit(typ chatevents.EventType, payload any) {
	_ = e.j.appendLocked(typ, payload)
}

func newTurnJournal(store *ai.AIStore, profileID, conversationID, turnID, runtimeID string, out io.Writer) *turnJournal {
	return &turnJournal{
		store: store, profileID: profileID, conversationID: conversationID, turnID: turnID,
		runtimeID: runtimeID, out: out, coalescer: chatevents.NewTextCoalescer(),
	}
}

func (j *turnJournal) print(v any) {
	b, _ := json.Marshal(v)
	_, _ = j.out.Write(append(b, '\n'))
}

// appendLocked commits one event and then prints it. A failed write is
// reported on stderr and the event is dropped: it is never printed as if
// it had been saved.
func (j *turnJournal) appendLocked(typ chatevents.EventType, payload any) error {
	ev, err := j.store.AppendEvent(j.profileID, j.conversationID, j.turnID, typ, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: journaling %s: %v\n", typ, err)
		return err
	}
	j.print(ev.Record())
	return nil
}

// start journals turn.started, before the runtime is even launched, with
// the files attached to the turn (copied into its folder already).
func (j *turnJournal) start(model, prompt string, attachments []chatevents.TurnAttachment) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appendLocked(chatevents.EventTurnStarted, chatevents.TurnStartedPayload{
		Backend: "agent", Runtime: j.runtimeID, Model: model, Text: prompt, Attachments: attachments,
	})
}

func (j *turnJournal) commitText(partID, text string) {
	if text != "" {
		_ = j.appendLocked(chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: partID, Text: text})
	}
}

// forceFlushLocked commits pending text and ends the current text part, so
// text after a tool call or notice starts a new one.
func (j *turnJournal) forceFlushLocked() {
	if partID, text, ok := j.coalescer.ForceFlush(); ok {
		j.commitText(partID, text)
	}
	j.currentPartID = ""
}

// tick flushes text that has waited out the coalescing window.
func (j *turnJournal) tick() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	// A timed flush only publishes what has arrived so far: the part goes
	// on until a tool call (forceFlushLocked) ends it, so a streamed reply
	// stays one text block instead of splitting mid-word.
	if partID, text, ok := j.coalescer.Flush(time.Now()); ok {
		j.commitText(partID, text)
	}
}

func (j *turnJournal) usageLocked(source string) {
	r := &j.usage
	if !r.HasInputTokens && !r.HasOutputTokens && !r.HasCostUSD {
		return
	}
	p := chatevents.UsageUpdatedPayload{Source: source}
	if r.HasInputTokens {
		v := r.InputTokens
		p.InputTokens = &v
	}
	if r.HasOutputTokens {
		v := r.OutputTokens
		p.OutputTokens = &v
	}
	if r.HasCostUSD {
		v := r.CostUSD
		p.CostUSD = &v
	}
	_ = j.appendLocked(chatevents.EventUsageUpdated, p)
}

// Notice codes for provider rate limits (agent-exec rev 20): each retry
// agent exec makes, and the turn's failure once it stops retrying.
const (
	noticeRateLimitRetry = "agent.rate_limit_retry"
	noticeRateLimited    = "agent.rate_limited"
)

// isRateLimitRetry reports whether ev is agent exec's notice that it is
// waiting out a 429 before retrying: {phase:"notice", message:"Rate
// limited (429) by <x>; retrying in <N>s (attempt <k>/3)"}.
func isRateLimitRetry(ev monomind.Event) bool {
	return ev.Phase == "notice" && strings.HasPrefix(ev.ErrMessage, "Rate limited (429)")
}

// handle journals one protocol event.
func (j *turnJournal) handle(ev monomind.Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	monomind.ApplyEventToResult(&j.usage, ev)

	// A native subagent's lifecycle and text are its own, not the lead's.
	if ev.Type == monomind.EventSubagent || (ev.Type == monomind.EventAssistant && ev.ParentToolUseID != "") {
		j.forceFlushLocked()
		j.subagents.Handle(lockedEmitter{j}, ev, "", "")
		return
	}

	switch ev.Type {
	case monomind.EventStart:
		j.markerMu.Lock()
		launcher := j.launcher
		j.markerMu.Unlock()
		if ev.Pid > 0 && launcher != nil && j.stopMarker == nil {
			ctx, cancel := context.WithCancel(context.Background())
			j.stopMarker = cancel
			j.markerDone = make(chan struct{})
			go func(runner int, launcher *pinnedLauncher, done chan struct{}) {
				defer close(done)
				m := learnTurnMarker(ctx, runner, launcher)
				j.markerMu.Lock()
				j.marker = m
				j.markerMu.Unlock()
			}(ev.Pid, launcher, j.markerDone)
		}
		// The badge the app shows on the turn; message is the status key.
		if ev.SandboxStatus != "" {
			_ = j.appendLocked(chatevents.EventNotice, chatevents.NoticePayload{Code: noticeAgentSandbox, Message: ev.SandboxStatus, Severity: chatevents.SeverityInfo})
			if detail := sandboxDetail(ev); detail != "" {
				_ = j.appendLocked(chatevents.EventNotice, chatevents.NoticePayload{Code: noticeAgentSandboxDetail, Message: detail, Severity: chatevents.SeverityInfo})
			}
		}
	case monomind.EventSession:
		if ev.SessionID != "" {
			_ = j.store.BindConversationSession(j.conversationID, j.profileID, j.runtimeID, ev.SessionID)
			_ = j.appendLocked(chatevents.EventSessionBound, chatevents.SessionBoundPayload{Runtime: j.runtimeID, SessionID: ev.SessionID})
		}
	case monomind.EventAssistant:
		if ev.Text != "" {
			if j.currentPartID == "" {
				j.partSeq++
				j.currentPartID = "part-" + strconv.Itoa(j.partSeq)
			}
			if partID, text, flushed := j.coalescer.Push(j.currentPartID, ev.Text, time.Now()); flushed {
				j.commitText(partID, text)
			} else {
				time.AfterFunc(chatevents.CoalesceWindow+10*time.Millisecond, j.tick)
			}
		}
	case monomind.EventToolCall:
		j.forceFlushLocked()
		_ = j.appendLocked(chatevents.EventToolStarted, chatevents.ToolStartedPayload{
			CallID: ev.ID, Name: ev.Name, Arguments: chatevents.RedactAndBoundJSON(ev.Args),
		})
	case monomind.EventToolResult:
		resultText := ""
		if ev.Result != nil {
			resultText = ev.Result.Text
		}
		bounded, _, _ := chatevents.BoundText(resultText, chatevents.MaxToolPreviewBytes)
		_ = j.appendLocked(chatevents.EventToolCompleted, chatevents.ToolCompletedPayload{CallID: ev.ID, OK: ev.OK, Result: bounded})
	case monomind.EventToolActivity:
		j.toolActivityLocked(ev)
	case monomind.EventStatus:
		if isRateLimitRetry(ev) {
			// Kept in the timeline (a coder.status line disappears once
			// the turn has output): the wait can come after tools ran.
			j.forceFlushLocked()
			_ = j.appendLocked(chatevents.EventNotice, chatevents.NoticePayload{Code: noticeRateLimitRetry, Message: ev.ErrMessage, Severity: chatevents.SeverityWarning})
		} else if msg := coderStatusMessage(ev, j.coderRuntime.ID); msg != "" {
			_ = j.appendLocked(chatevents.EventNotice, chatevents.NoticePayload{Code: noticeCoderStatus, Message: msg, Severity: chatevents.SeverityInfo})
		}
	case monomind.EventDone:
		j.backgroundPids = ev.BackgroundPids
	case monomind.EventUsage:
		j.usageLocked("usage")
	case monomind.EventResult:
		j.usageLocked("result")
		j.resultNoticesLocked(ev)
	case monomind.EventError:
		if ev.Code == monomind.ErrRateLimited {
			// agent exec gave up retrying; its message says why and what
			// to do ("Rate limited by <model> (429) after 3 attempts. …").
			j.forceFlushLocked()
			msg, _, _ := chatevents.BoundText(ev.ErrMessage, chatevents.MaxToolPreviewBytes)
			_ = j.appendLocked(chatevents.EventNotice, chatevents.NoticePayload{Code: noticeRateLimited, Message: msg, Severity: chatevents.SeverityError})
		} else if !ev.Fatal {
			j.forceFlushLocked()
			msg, _, _ := chatevents.BoundText(ev.ErrMessage, chatevents.MaxToolPreviewBytes)
			_ = j.appendLocked(chatevents.EventNotice, chatevents.NoticePayload{Code: ev.Code, Message: msg, Severity: chatevents.SeverityWarning})
		}
	}
}

// finish flushes pending text and commits turn.finished once. When that
// write fails, the event is still printed, live-only: seq MaxSafeSeq so
// the UI never drops it as stale, and historySaved false.
func (j *turnJournal) finish(stopRequested bool, res *monomind.TurnResult) {
	code := ""
	if !stopRequested && res != nil && res.Err != nil && monomind.IsAgentNotSetup(res.Err) {
		code = monomind.AgentNotSetupCode
	}
	j.finishCode(stopRequested, res, code)
}

// finishCode is finish with turn.finished's failure code given.
func (j *turnJournal) finishCode(stopRequested bool, res *monomind.TurnResult, code string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	j.forceFlushLocked()
	j.closeOpenNativeCallsLocked()
	j.finished = true
	if j.stopMarker != nil {
		// A turn that ends at once may end before the lookup does: give it a moment, then stop it
		// and wait for its answer, so the marker read below is the lookup's last word.
		select {
		case <-j.markerDone:
		case <-time.After(300 * time.Millisecond):
		}
		j.stopMarker()
		<-j.markerDone
	}
	j.markerMu.Lock()
	j.launcher.close()
	j.markerMu.Unlock()
	// The runtime's own helpers (e.g. the MCP servers opencode starts) are
	// still alive when it reports done and exit with it; only processes
	// that outlive the runtime are worth a warning.
	if pids := stillRunning(j.backgroundPids, backgroundSettle); len(pids) > 0 {
		j.markerMu.Lock()
		root := j.marker
		j.markerMu.Unlock()
		_ = j.appendLocked(chatevents.EventNotice, backgroundNoticeFor(root, pids))
	}

	status, reason := chatevents.ComputeTurnStatus(stopRequested, res)
	var exitCode *int
	sandbox := ""
	if res != nil {
		v := res.ExitCode
		exitCode = &v
		sandbox = res.SandboxStatus
	}
	ev, already, err := j.store.FinalizeTurnCode(j.profileID, j.conversationID, j.turnID, status, reason, code, exitCode, true, sandbox)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: finalizing turn %s: %v\n", j.turnID, err)
		live, buildErr := chatevents.New(j.profileID, j.conversationID, j.turnID, chatevents.MaxSafeSeq, time.Now(), chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{
			Status: status, Reason: reason, Code: code, ExitCode: exitCode, HistorySaved: false, Sandbox: sandbox,
		})
		if buildErr == nil {
			j.print(live.Record())
		}
	case already:
		// Finalized elsewhere (e.g. `chat history finish`); nothing to add.
	default:
		j.print(ev.Record())
	}
}

// fail finalizes the turn as failed because of err, raised before or
// instead of a protocol result.
func (j *turnJournal) fail(err error) {
	code := ""
	if monomind.IsAgentNotSetup(err) {
		code = monomind.AgentNotSetupCode
	}
	j.finishCode(false, &monomind.TurnResult{Err: &monomind.ProtocolError{Code: monomind.ErrRunnerError, Message: err.Error()}}, code)
}

// sandboxDetail is the extra line a start event's sandbox verdict carries:
// the applied mode for a restricted turn, else the fallback note.
func sandboxDetail(ev monomind.Event) string {
	if ev.SandboxStatus == monomind.SandboxStatusRestricted && ev.SandboxApplied != "" {
		return ev.SandboxApplied
	}
	return ev.SandboxNote
}

// setLauncher records the pid of the process this turn's launcher started: the anchor the turn's
// marker is learned from. It is monomind.Exec's own report, not the runner's.
func (j *turnJournal) setLauncher(pid int) {
	l := pinLauncher(pid)
	j.markerMu.Lock()
	j.launcher = l
	j.markerMu.Unlock()
}
