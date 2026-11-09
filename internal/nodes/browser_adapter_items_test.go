package nodes

import (
	"context"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/workflow"
)

// confirmedPkg is a package confirmed for live runs, so its message action
// passes checkLiveRun.
type confirmedPkg struct{ outPkg }

func (confirmedPkg) LiveRunConfirmed() bool { return true }

// sendSource serves acme/send: one bot call per target, mark_failed on error.
// With passThrough the bot result is not a map, so nothing is extracted.
type sendSource struct{}

func (sendSource) Load(a, t string) ([]byte, error) {
	if a != "acme" || t != "send" {
		return nil, fmt.Errorf("not found %s/%s", a, t)
	}
	return []byte(`{"actionType":"send","automation":"acme","sideEffects":"message",
	  "steps":[{"id":"send","type":"call_bot_method","methodName":"send","args":["{{item.username}}"],
	    "sideEffect":true,"onError":{"action":"mark_failed"}}],
	  "loops":[{"id":"each","iterator":"selectedListItems","steps":["send"]}]}`), nil
}
func (sendSource) List() ([]string, error) { return []string{"acme/send"}, nil }
func (sendSource) Package(string) action.PackageContext {
	return confirmedPkg{outPkg{native: "acme"}}
}

// flakyBot fails each target in fail once (or always with failAlways) and
// records every send that went through.
type flakyBot struct {
	fail        map[string]bool
	failAlways  bool
	passThrough bool
	sent        []string
}

func (b *flakyBot) GetMethodByName(name string) (func(context.Context, ...interface{}) (interface{}, error), bool) {
	if name != "send" {
		return nil, false
	}
	return func(_ context.Context, args ...interface{}) (interface{}, error) {
		to := fmt.Sprint(args[1])
		if b.fail[to] {
			if !b.failAlways {
				delete(b.fail, to)
			}
			return nil, errors.New("send button never appeared")
		}
		b.sent = append(b.sent, to)
		if b.passThrough {
			return true, nil
		}
		return map[string]interface{}{"to": to, "status": "sent"}, nil
	}, true
}

type botRegistry struct{ bot action.BotAdapter }

func (r botRegistry) GetAdapter(string) (action.BotAdapter, bool) { return r.bot, true }

func setupSend(t *testing.T, bot *flakyBot) {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(sendSource{})
	prevSP, prevBR := globalSessionProvider, globalBotRegistry
	SetGlobalSessionProvider(pageProvider{})
	SetGlobalBotRegistry(botRegistry{bot: bot})
	t.Cleanup(func() {
		action.SetDefSource(nil)
		SetGlobalSessionProvider(prevSP)
		SetGlobalBotRegistry(prevBR)
	})
}

func sendInput(items ...workflow.Item) workflow.NodeInput {
	return workflow.NodeInput{Items: items, ExecutionID: "e1", NodeID: "n1"}
}

func sendConfig(targets ...interface{}) map[string]interface{} {
	return map[string]interface{}{"targets": targets}
}

func sentTo(outs []workflow.NodeOutput) []interface{} {
	var to []interface{}
	for _, o := range outs {
		for _, it := range o.Items {
			to = append(to, it.JSON["to"])
		}
	}
	return to
}

// Three targets, the second fails: the attempt reports a partial failure
// carrying the two sends that worked; the retry (same node run, as the
// engine's executeWithRetry makes it) sends only to the second, and the
// merged outputs hold all three.
func TestBrowserNode_RetryRunsOnlyFailedTargets(t *testing.T) {
	bot := &flakyBot{fail: map[string]bool{"b": true}}
	setupSend(t, bot)
	ctx := workflow.WithNodeState(context.Background(), nil)
	in := sendInput(workflow.NewItem(map[string]interface{}{"row": 1.0}))
	node := NewBrowserNode("acme", "send")

	out, err := node.Execute(ctx, in, sendConfig("a", "b", "c"))
	var pe *workflow.PartialItemsError
	if out != nil || !errors.As(err, &pe) {
		t.Fatalf("attempt 1: out=%v err=%v, want a partial failure", out, err)
	}
	if !reflect.DeepEqual(pe.FailedIndexes, []int{0}) || len(pe.Errs) != 1 {
		t.Errorf("failed indexes = %v errs = %v", pe.FailedIndexes, pe.Errs)
	}
	if got := sentTo(pe.Succeeded); !reflect.DeepEqual(got, []interface{}{"a", "c"}) {
		t.Errorf("succeeded outputs = %v, want a and c", got)
	}

	retry, err := node.Execute(ctx, in, sendConfig("a", "b", "c"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !reflect.DeepEqual(bot.sent, []string{"a", "c", "b"}) {
		t.Errorf("sends = %v: the retry re-sent to a target that already succeeded", bot.sent)
	}
	merged := append(append([]interface{}{}, sentTo(pe.Succeeded)...), sentTo(retry)...)
	if !reflect.DeepEqual(merged, []interface{}{"a", "c", "b"}) {
		t.Errorf("merged outputs = %v", merged)
	}
	if st := workflow.NodeStateFromContext(ctx); st != nil {
		t.Errorf("progress kept after the run succeeded: %s", st)
	}
}

// A send action that extracts nothing passes its input through: a partial
// failure carries no output (the item is not done), the finishing retry
// passes it through once.
func TestBrowserNode_PartialPassThroughWaitsForRetry(t *testing.T) {
	bot := &flakyBot{fail: map[string]bool{"a": true}, passThrough: true}
	setupSend(t, bot)
	ctx := workflow.WithNodeState(context.Background(), nil)
	in := sendInput(workflow.NewItem(map[string]interface{}{"row": 1.0}))
	node := NewBrowserNode("acme", "send")

	_, err := node.Execute(ctx, in, sendConfig("a", "b"))
	var pe *workflow.PartialItemsError
	if !errors.As(err, &pe) || pe.Succeeded != nil {
		t.Fatalf("attempt 1: err=%v, want a partial failure with no outputs", err)
	}
	out, err := node.Execute(ctx, in, sendConfig("a", "b"))
	if err != nil || len(out) != 1 || len(out[0].Items) != 1 || out[0].Items[0].JSON["row"] != 1.0 {
		t.Fatalf("retry: out=%v err=%v, want the input passed through", out, err)
	}
	if !reflect.DeepEqual(bot.sent, []string{"b", "a"}) {
		t.Errorf("sends = %v", bot.sent)
	}
}

// Every target failing keeps the plain error; every target working keeps
// the plain outputs.
func TestBrowserNode_AllFailedOrAllSucceededUnchanged(t *testing.T) {
	bot := &flakyBot{fail: map[string]bool{"a": true, "b": true}, failAlways: true}
	setupSend(t, bot)
	ctx := workflow.WithNodeState(context.Background(), nil)
	in := sendInput(workflow.NewItem(map[string]interface{}{}))
	_, err := NewBrowserNode("acme", "send").Execute(ctx, in, sendConfig("a", "b"))
	var pe *workflow.PartialItemsError
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("all failed: err=%v, want a plain error", err)
	}

	bot.fail = nil
	out, err := NewBrowserNode("acme", "send").Execute(ctx, in, sendConfig("a", "b"))
	if err != nil || !reflect.DeepEqual(sentTo(out), []interface{}{"a", "b"}) {
		t.Fatalf("all succeeded: out=%v err=%v", sentTo(out), err)
	}
}

// With no input item to name, the failure stays a plain error, but the
// engine's whole-node retry still skips the targets already done. A
// different target list (a later loop pass) starts afresh.
func TestBrowserNode_NoInputItemsStillDedupes(t *testing.T) {
	bot := &flakyBot{fail: map[string]bool{"b": true}}
	setupSend(t, bot)
	ctx := workflow.WithNodeState(context.Background(), nil)
	node := NewBrowserNode("acme", "send")

	_, err := node.Execute(ctx, sendInput(), sendConfig("a", "b"))
	var pe *workflow.PartialItemsError
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("attempt 1: err=%v, want a plain error", err)
	}
	if _, err := node.Execute(ctx, sendInput(), sendConfig("a", "b")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !reflect.DeepEqual(bot.sent, []string{"a", "b"}) {
		t.Errorf("sends = %v", bot.sent)
	}

	bot.fail = map[string]bool{"z": true}
	_, _ = node.Execute(ctx, sendInput(), sendConfig("a", "z"))
	_, _ = node.Execute(ctx, sendInput(), sendConfig("a", "c"))
	if want := []string{"a", "b", "a", "a", "c"}; !reflect.DeepEqual(bot.sent, want) {
		t.Errorf("sends = %v, want %v", bot.sent, want)
	}
}

// The all-failed error reads as before this change; a partial failure's
// per-item error keeps that prefix and says how many targets were done.
func TestBrowserNode_FailureMessages(t *testing.T) {
	bot := &flakyBot{fail: map[string]bool{"a": true, "b": true}, failAlways: true}
	setupSend(t, bot)
	in := sendInput(workflow.NewItem(map[string]interface{}{}))
	_, err := NewBrowserNode("acme", "send").Execute(workflow.WithNodeState(context.Background(), nil), in, sendConfig("a", "b"))
	const prefix = "nodes: BrowserNode execute acme/send: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || strings.Contains(err.Error(), "target(s) done") {
		t.Fatalf("all failed: err = %v, want the plain %q error", err, prefix)
	}

	bot.fail = map[string]bool{"b": true}
	_, err = NewBrowserNode("acme", "send").Execute(workflow.WithNodeState(context.Background(), nil), in, sendConfig("a", "b", "c"))
	var pe *workflow.PartialItemsError
	if !errors.As(err, &pe) || len(pe.Errs) != 1 {
		t.Fatalf("partial: err = %v, want a partial failure", err)
	}
	if msg := pe.Errs[0].Error(); !strings.HasPrefix(msg, prefix+"2 target(s) done, the rest failed: ") {
		t.Errorf("partial item error = %q", msg)
	}
}
