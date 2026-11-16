package doomsday

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

var (
	pinA = Pin{Provider: "claude", Model: "opus", Version: "1"}
	pinB = Pin{Provider: "codex", Model: "gpt", Version: "2"}
)

func TestBuildRunCharter(t *testing.T) {
	c, err := BuildRunCharter(RunSpec{Goal: " ship it ", BudgetUSD: 2.5, Criteria: []string{"it works", " "}, Boss: pinA, Verifier: pinB})
	if err != nil {
		t.Fatal(err)
	}
	if c.Goal != "ship it" || c.Limits.BudgetMicro != 2500000 || c.Limits.NoCeiling || len(c.Criteria) != 1 || c.Pins.Judging != "default" {
		t.Fatalf("charter: %+v", c)
	}
	if len(c.Lines) != len(EverythingLines()) || c.Has(LinePlatformCaps) {
		t.Fatalf("lines: %v", c.Lines)
	}
	if c.Limits.TreeDepth != DefaultTreeDepth || c.Limits.MaxTotal != DefaultMaxTotal {
		t.Fatalf("limits not defaulted: %+v", c.Limits)
	}
}

func TestBuildRunCharterRefusals(t *testing.T) {
	base := RunSpec{Goal: "g", Boss: pinA, Verifier: pinB}
	for name, s := range map[string]RunSpec{
		"no budget choice": base,
		"both budgets":     {Goal: "g", Boss: pinA, Verifier: pinB, BudgetUSD: 1, NoCeiling: true},
		"empty goal":       {Boss: pinA, Verifier: pinB, BudgetUSD: 1},
		"verifier is boss": {Goal: "g", Boss: pinA, Verifier: pinA, BudgetUSD: 1},
		"unknown lift":     {Goal: "g", Boss: pinA, Verifier: pinB, BudgetUSD: 1, Lift: []Line{"nope"}},
	} {
		if _, err := BuildRunCharter(s); err == nil {
			t.Errorf("%s: no refusal", name)
		}
	}
	s := base
	s.NoCeiling = true
	if c, err := BuildRunCharter(s); err != nil || !c.Limits.NoCeiling {
		t.Fatalf("no-ceiling: %v %+v", err, c.Limits)
	}
	s.Lift = []Line{LineSpending}
	if c, _ := BuildRunCharter(s); len(c.Lines) != 1 || !c.Has(LineSpending) {
		t.Fatalf("--lift not honoured: %v", c.Lines)
	}
}

func TestParsePin(t *testing.T) {
	if p, err := ParsePin("claude/opus@4.1"); err != nil || p != (Pin{"claude", "opus", "4.1"}) {
		t.Fatalf("%v %+v", err, p)
	}
	if p, err := ParsePin("codex/gpt-5"); err != nil || p.Version != "unknown" {
		t.Fatalf("%v %+v", err, p)
	}
	for _, bad := range []string{"", "opus", "/opus", "claude/"} {
		if _, err := ParsePin(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPickPins(t *testing.T) {
	b, v, err := PickPins([]Pin{pinA, pinA, pinB})
	if err != nil || b != pinA || v != pinB {
		t.Fatalf("%v %+v %+v", err, b, v)
	}
	if _, _, err := PickPins([]Pin{pinA, pinA}); err == nil {
		t.Fatal("one distinct model gave a verifier")
	}
	if _, _, err := PickPins(nil); err == nil {
		t.Fatal("no models accepted")
	}
}

type openedChat struct{ runtime, model, cwd string }

// runRig is a chat rig whose deps can make the run's chat and list models; opened records what the chat was made with.
func runRig(t *testing.T, choices []Pin) (*chatRig, *[]openedChat) {
	t.Helper()
	c := newChatRig(t, guiCharter)
	var opened []openedChat
	c.deps.OpenChat = func(_ *cobra.Command, rt, model, cwd string) (string, error) {
		opened = append(opened, openedChat{rt, model, cwd})
		return "front-1", nil
	}
	c.deps.PinChoices = func(context.Context) ([]Pin, error) { return choices, nil }
	return c, &opened
}

func TestRunStartsLikeTheAppFromAGoal(t *testing.T) {
	c, opened := runRig(t, []Pin{pinA, pinB})
	out, err := run(t, c.deps, "", "run", "fix the thing", "--budget", "2", "--criterion", "it is fixed", "--cwd", "/work")
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got := c.steps(); got != "lock,make,budget,check,enter,bind,turn" {
		t.Fatalf("steps = %s", got)
	}
	if len(*opened) != 1 || (*opened)[0] != (openedChat{"claude", "opus", "/work"}) {
		t.Fatalf("chat opened with %+v", *opened)
	}
	if len(c.eng.enters) != 1 || c.eng.enters[0].Kind != "script" {
		t.Fatalf("acknowledgement: %+v", c.eng.enters)
	}
	ch := c.eng.canon
	if ch.Goal != "fix the thing" || ch.Limits.BudgetMicro != 2000000 || ch.Pins.Boss != pinA || ch.Pins.Verifier != pinB || len(ch.Criteria) != 1 {
		t.Fatalf("charter: %+v", ch)
	}
	if len(c.chat.budgets) != 1 || c.chat.budgets[0] != 2 || len(c.chat.turns) != 1 || c.chat.turns[0].Goal != "fix the thing" {
		t.Fatalf("boss budget %v, turns %+v", c.chat.budgets, c.chat.turns)
	}
}

func TestRunRefusalsMakeNoChat(t *testing.T) {
	for name, args := range map[string][]string{
		"no budget choice": {"run", "g"},
		"both budgets":     {"run", "g", "--budget", "1", "--no-ceiling"},
		"a bad budget":     {"run", "g", "--budget", "abc"},
		"a bad model":      {"run", "g", "--budget", "1", "--boss", "opus"},
	} {
		c, opened := runRig(t, []Pin{pinA, pinB})
		if _, err := run(t, c.deps, "", args...); err == nil {
			t.Errorf("%s: started", name)
		}
		if len(*opened) != 0 || len(c.eng.enters) != 0 {
			t.Errorf("%s: a chat or a run was made", name)
		}
	}
	// not the user's: refused before anything
	c, opened := runRig(t, []Pin{pinA, pinB})
	c.refusal = "an agent is running this command"
	if _, err := run(t, c.deps, "", "run", "g", "--budget", "1"); err == nil || len(*opened) != 0 {
		t.Fatalf("an agent's run was not refused: %v", err)
	}
	// an install that cannot make the chat
	c, _ = runRig(t, []Pin{pinA, pinB})
	c.deps.OpenChat = nil
	if _, err := run(t, c.deps, "", "run", "g", "--budget", "1"); err == nil || len(c.eng.enters) != 0 {
		t.Fatalf("a run started without a way to make its chat: %v", err)
	}
}

func TestResolvePins(t *testing.T) {
	d := Deps{PinChoices: func(context.Context) ([]Pin, error) { return []Pin{pinA, pinB}, nil }}
	ctx := context.Background()

	s := RunSpec{}
	if err := d.resolvePins(ctx, &s, "", ""); err != nil || s.Boss != pinA || s.Verifier != pinB {
		t.Fatalf("from the roster: %v %+v", err, s)
	}
	s = RunSpec{}
	if err := d.resolvePins(ctx, &s, "claude/opus@1", ""); err != nil || s.Boss != pinA || s.Verifier != pinB {
		t.Fatalf("boss given, verifier the other: %v %+v", err, s)
	}
	s = RunSpec{}
	if err := d.resolvePins(ctx, &s, "", "claude/opus@1"); err != nil || s.Verifier != pinA || s.Boss != pinB {
		t.Fatalf("verifier given, boss the other: %v %+v", err, s)
	}
	s = RunSpec{}
	if err := d.resolvePins(ctx, &s, "a/b@1", "c/d@2"); err != nil || s.Boss.Provider != "a" || s.Verifier.Provider != "c" {
		t.Fatalf("both given: %v %+v", err, s)
	}
	// both given: the roster is never read
	d2 := Deps{PinChoices: func(context.Context) ([]Pin, error) { return nil, errors.New("read") }}
	s = RunSpec{}
	if err := d2.resolvePins(ctx, &s, "a/b@1", "c/d@2"); err != nil {
		t.Fatalf("the roster was read: %v", err)
	}
	// a roster failure, an empty roster and no roster seam all refuse
	if err := d2.resolvePins(ctx, &RunSpec{}, "", ""); err == nil {
		t.Fatal("a roster failure was swallowed")
	}
	if err := (Deps{}).resolvePins(ctx, &RunSpec{}, "", ""); err == nil {
		t.Fatal("no roster seam, no flags: accepted")
	}
	only := Deps{PinChoices: func(context.Context) ([]Pin, error) { return []Pin{pinA}, nil }}
	if err := only.resolvePins(ctx, &RunSpec{}, "claude/opus@1", ""); err == nil {
		t.Fatal("a verifier equal to the boss was chosen")
	}
}
