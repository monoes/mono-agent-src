package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

const resultFieldsTranscript = `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"assistant","text":"done"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"done","effort":"high","peak_context_tokens":250000,"context_warning":true,"unexpected_models":["claude-opus-4-1"],"model_usage":{"claude-opus-4-1":{"input":1,"output":2,"cache_read":3,"cache_creation":4}},"agent_launches":{"total":2,"review":1}}'
  echo '{"v":1,"type":"done","exit_code":0}'`

func TestCoderTurnJournalsUnexpectedModelsAndContextWarning(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, _ := writeCoderMonomind(t, resultFieldsTranscript)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	var got []string
	for _, n := range parseJournaledTurn(t, out).byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(n.Payload, &p)
		if p.Code == noticeCoderResult {
			got = append(got, p.Message)
			if p.Severity != chatevents.SeverityWarning {
				t.Errorf("severity = %v", p.Severity)
			}
		}
	}
	if len(got) != 2 || !strings.Contains(got[0], "claude-opus-4-1") || !strings.Contains(got[1], "250000") {
		t.Errorf("coder.result notices = %q", got)
	}
}

func TestResultFieldsRoundTrip(t *testing.T) {
	var ev monomind.Event
	line := `{"v":1,"type":"result","effort":"high","peak_context_tokens":5,"agent_launches":{"total":2,"review":1},"model_usage":{"m":{"input":1,"output":2,"cache_read":3,"cache_creation":4}}}`
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Effort != "high" || ev.PeakContextTokens != 5 || ev.AgentLaunches == nil || ev.AgentLaunches.Review != 1 || ev.ModelUsage["m"].CacheCreation != 4 {
		t.Errorf("decoded %+v", ev.ResultFields)
	}
	b, _ := json.Marshal(ev)
	if !strings.Contains(string(b), `"agent_launches":{"total":2,"review":1}`) || strings.Contains(string(b), "context_warning") {
		t.Errorf("re-encoded %s", b)
	}
}

func TestCoderTurnCompactsAWarnedSessionInsteadOfWarning(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	transcript := `  echo '{"v":1,"type":"session","session_id":"sess-1"}'
` + resultFieldsTranscript
	bin, argsLog := writeCoderMonomind(t, transcript)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "--resume sess-1") {
		t.Errorf("the session was not resumed to compact it: %q", args)
	}
	compacted := false
	for _, n := range parseJournaledTurn(t, out).byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(n.Payload, &p)
		if p.Code == noticeCoderResult && strings.Contains(p.Message, "every further call") {
			t.Errorf("the warning still shows: %q", p.Message)
		}
		compacted = compacted || strings.Contains(p.Message, "was compacted")
	}
	if !compacted {
		t.Errorf("no compaction notice:\n%s", out)
	}
}

// compactHarness runs compactContext over a journal whose fake monomind
// answers every call with transcript; it returns the journaled notices
// (code:message), the usage.updated costs and the fake's argv log.
type compactHarness struct {
	j       *turnJournal
	store   *ai.AIStore
	conv    string
	opts    monomind.ExecOptions
	argsLog string
}

func newCompactHarness(t *testing.T, transcript string, peak int64) *compactHarness {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, transcript)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	store.CreateTurn(conv.ID, "default", "t1", "", "hi")
	var out bytes.Buffer
	j := newTurnJournal(store, "default", conv.ID, "t1", "claude", &out)
	j.contextPeak = peak
	return &compactHarness{j: j, store: store, conv: conv.ID, argsLog: argsLog,
		opts: monomind.ExecOptions{Bin: bin, Runtime: "claude", Cwd: t.TempDir(), Timeout: time.Hour}}
}

func (h *compactHarness) events(t *testing.T) (notices []string, costs []float64) {
	t.Helper()
	evs, _ := h.store.GetEvents(h.conv, "t1", "default", 0, 100)
	for _, ev := range evs {
		switch ev.Type {
		case chatevents.EventNotice:
			var p chatevents.NoticePayload
			json.Unmarshal(ev.Payload, &p)
			notices = append(notices, p.Code+": "+p.Message)
		case chatevents.EventUsageUpdated:
			var p chatevents.UsageUpdatedPayload
			json.Unmarshal(ev.Payload, &p)
			if p.CostUSD != nil {
				costs = append(costs, *p.CostUSD)
			}
		}
	}
	return
}

func (h *compactHarness) argv() string {
	b, _ := os.ReadFile(h.argsLog)
	return string(b)
}

func lastNotice(n []string) string {
	if len(n) == 0 {
		return ""
	}
	return n[len(n)-1]
}

const compactOKTranscript = `  echo '{"v":1,"type":"session","session_id":"sess-1"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","cost_usd":0.25}'
  echo '{"v":1,"type":"done","exit_code":0}'`

func TestCompactIgnoredWhenSessionIDChanges(t *testing.T) {
	h := newCompactHarness(t, strings.Replace(compactOKTranscript, "sess-1", "sess-other", 1), 250000)
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	notices, _ := h.events(t)
	if got := lastNotice(notices); !strings.Contains(got, "every further call") || strings.Contains(got, "was compacted") {
		t.Errorf("a compaction that moved to another session was claimed: %q", notices)
	}
}

func TestCompactJournalsItsCostIntoTheTurnTotal(t *testing.T) {
	h := newCompactHarness(t, compactOKTranscript, 250000)
	h.j.usage.CostUSD, h.j.usage.HasCostUSD = 0.5, true
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	notices, costs := h.events(t)
	if len(costs) != 1 || costs[0] != 0.75 {
		t.Errorf("usage costs = %v, want the turn's 0.5 plus the compaction's 0.25", costs)
	}
	if !strings.Contains(lastNotice(notices), "was compacted") {
		t.Errorf("notices = %q", notices)
	}
}

func TestCompactRespectsWhatTheTurnLeftOfItsBudget(t *testing.T) {
	h := newCompactHarness(t, compactOKTranscript, 250000)
	h.opts.BudgetUSD = 1
	h.j.usage.CostUSD, h.j.usage.HasCostUSD = 0.4, true
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	if !strings.Contains(h.argv(), "--budget-usd 0.6") {
		t.Errorf("the compaction was not capped at the remainder: %q", h.argv())
	}

	h = newCompactHarness(t, compactOKTranscript, 250000)
	h.opts.BudgetUSD = 1
	h.j.usage.CostUSD, h.j.usage.HasCostUSD = 1, true
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	notices, _ := h.events(t)
	if strings.Contains(h.argv(), "--resume") || !strings.Contains(lastNotice(notices), "every further call") {
		t.Errorf("a spent budget still compacted: argv %q notices %q", h.argv(), notices)
	}
}

// A capped turn whose runtime reported no cost cannot say what is left of the
// cap, so the compaction (which would otherwise get the whole cap again) does
// not run; an uncapped turn still compacts.
func TestCompactIsSkippedUnderACapWhenNoCostWasReported(t *testing.T) {
	h := newCompactHarness(t, compactOKTranscript, 250000)
	h.opts.BudgetUSD = 1
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	notices, _ := h.events(t)
	if strings.Contains(h.argv(), "--resume") || strings.Contains(h.argv(), "--budget-usd") {
		t.Errorf("a compaction ran with the full cap: %q", h.argv())
	}
	if got := lastNotice(notices); !strings.Contains(got, "every further call") || strings.Contains(got, "was compacted") {
		t.Errorf("notices = %q", notices)
	}
	h = newCompactHarness(t, compactOKTranscript, 250000)
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	if !strings.Contains(h.argv(), "--resume") {
		t.Errorf("an uncapped turn no longer compacts: %q", h.argv())
	}
}

func TestCompactAfterStopSaysNothing(t *testing.T) {
	h := newCompactHarness(t, compactOKTranscript, 250000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	compactContext(ctx, h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, nil)
	notices, _ := h.events(t)
	if len(notices) != 0 || strings.Contains(h.argv(), "--resume") {
		t.Errorf("a stopped turn compacted or warned: notices %q argv %q", notices, h.argv())
	}
}

func TestWarnedTurnThatErroredStillGetsTheWarning(t *testing.T) {
	h := newCompactHarness(t, compactOKTranscript, 250000)
	compactContext(context.Background(), h.j, h.opts, &monomind.TurnResult{SessionID: "sess-1"}, errors.New("exec failed"))
	notices, _ := h.events(t)
	if !strings.Contains(lastNotice(notices), "250000") || strings.Contains(h.argv(), "--resume") {
		t.Errorf("notices %q argv %q", notices, h.argv())
	}
}
