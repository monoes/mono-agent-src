package doomsday

import (
	"context"
	"errors"
	"os"
	"strings"
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
	named := Pin{Provider: "claude", Model: "claude-opus-5-5", Version: "1"}
	if b, v, err := PickPins([]Pin{pinA, named, pinB}); err != nil || b != pinA || v != pinB {
		t.Fatalf("the boss verified by its own model's other name: %v %+v %+v", err, b, v)
	}
	if c, ok := otherModel([]Pin{pinA, named, pinB}, named); !ok || c != pinB {
		t.Fatalf("the other pin of a given one: %+v %v", c, ok)
	}
	other := Pin{Provider: "claude", Model: "opus", Version: "2"}
	if _, err := BuildRunCharter(RunSpec{Goal: "g", Boss: named, Verifier: other, BudgetUSD: 1}); err == nil {
		t.Fatal("a charter let the boss's own model verify it")
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

func TestRunWhileARunExistsMakesNoChat(t *testing.T) {
	c, opened := runRig(t, []Pin{pinA, pinB})
	c.eng.active = true
	if _, err := run(t, c.deps, "", "run", "g", "--budget", "1"); err == nil {
		t.Fatal("a second run started")
	}
	if len(*opened) != 0 || len(c.eng.enters) != 0 {
		t.Fatalf("a chat (%d) or a run (%d) was made", len(*opened), len(c.eng.enters))
	}
}

func TestRunRefusesAnEmptyLiftAndTheRemovedChatFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"an empty --lift":     {"run", "g", "--budget", "1", "--lift", ""},
		"a blank --lift line": {"run", "g", "--budget", "1", "--lift", " , "},
		"--runtime":           {"run", "g", "--budget", "1", "--runtime", "x"},
		"--model":             {"run", "g", "--budget", "1", "--model", "x"},
	} {
		c, opened := runRig(t, []Pin{pinA, pinB})
		if _, err := run(t, c.deps, "", args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(*opened) != 0 || len(c.eng.enters) != 0 {
			t.Errorf("%s: a chat or a run was made", name)
		}
	}
	c, _ := runRig(t, []Pin{pinA, pinB})
	if _, err := run(t, c.deps, "", "run", "g", "--budget", "1", "--lift", "spending"); err != nil {
		t.Fatalf("a named line was refused: %v", err)
	}
	if got := c.eng.canon.Lines; len(got) != 1 || got[0] != LineSpending {
		t.Fatalf("lifted lines: %v", got)
	}
}

func TestRunChatIsTheBossPin(t *testing.T) {
	c, opened := runRig(t, nil)
	if _, err := run(t, c.deps, "", "run", "g", "--budget", "1", "--boss", "a/m1@1", "--verifier", "b/m2@1", "--cwd", "/w"); err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 1 || (*opened)[0] != (openedChat{"a", "m1", "/w"}) {
		t.Fatalf("the chat is not the boss's: %+v", *opened)
	}
}

func TestRunChecksTheMachineBeforeReadingTheRoster(t *testing.T) {
	c, opened := runRig(t, []Pin{pinA, pinB})
	c.eng.containment = errors.New("containment unavailable: test")
	read := false
	c.deps.PinChoices = func(context.Context) ([]Pin, error) { read = true; return []Pin{pinA, pinB}, nil }
	if _, err := run(t, c.deps, "", "run", "g", "--budget", "1"); err == nil {
		t.Fatal("started on a machine without containment")
	}
	if read || len(*opened) != 0 {
		t.Fatalf("roster read %v, chats %d", read, len(*opened))
	}
}

func TestParseBudgetUSD(t *testing.T) {
	if v, err := ParseBudgetUSD(" 2.5 "); err != nil || v != 2.5 {
		t.Fatalf("%v %v", v, err)
	}
	for _, bad := range []string{"", "abc", "0", "-1", "NaN", "Inf", "+Inf", "1e30", "0.0000001", "1000000.01"} {
		if v, err := ParseBudgetUSD(bad); err == nil {
			t.Errorf("%q accepted as %v", bad, v)
		}
	}
}

func TestDefaultRunDirRefusesHomeAndRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	t.Chdir(home)
	if _, err := defaultRunDir(); err == nil {
		t.Fatal("the home folder was taken as the run's folder")
	}
	t.Chdir(t.TempDir())
	if dir, err := defaultRunDir(); err != nil || dir == "" {
		t.Fatalf("a scratch folder was refused: %v", err)
	}
}

func TestBuildRunCharterRefusesHiddenAndLongText(t *testing.T) {
	base := RunSpec{Boss: pinA, Verifier: pinB, BudgetUSD: 1}
	for name, goal := range map[string]string{
		"a zero-width character": "ship\u200b it",
		"a bidi override":        "ship \u202eit",
		"too long":               strings.Repeat("a", MaxDisplayRunes+1),
	} {
		s := base
		s.Goal = goal
		if _, err := BuildRunCharter(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := base
	s.Goal = "ship it\nand test it"
	if _, err := BuildRunCharter(s); err != nil {
		t.Fatalf("a newline in the goal was refused: %v", err)
	}
}
