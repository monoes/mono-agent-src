package doomsday

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// The start from a chat (`start --chat CHAT`): the boss is made, given its budget, checked, the run entered, the boss
// recorded and its first turn queued, and every failure undoes exactly what the steps before it made.

// chatEngine is a fakeEngine that can check and record a boss, report a run, and fail at a chosen step. All calls go to log.
type chatEngine struct {
	fakeEngine
	log           *[]string
	active        bool // a run exists (Status)
	activeNow     func() bool
	checkErr      error
	enterErr      error
	enterRuns     bool // Enter fails AFTER the run exists
	enterRecorded bool
	bindErr       error
	started       bool
	hash          string // the charter hash the entry was attempted with
	statusErr     error  // Status fails once started or after a failed Enter
	otherRun      bool   // the run that exists is another start's
	onCheck       func()
	enterCalls    int
}

func (c *chatEngine) CheckBoss(context.Context, int64) error {
	*c.log = append(*c.log, "check")
	return c.checkErr
}
func (c *chatEngine) BindBoss(context.Context, int64, Entry) error {
	*c.log = append(*c.log, "bind")
	return c.bindErr
}
func (c *chatEngine) Enter(ctx context.Context, cn Canon, h string, a Ack) (Entry, error) {
	*c.log = append(*c.log, "enter")
	c.hash = h
	c.enterCalls++
	if c.enterErr != nil {
		c.started = c.enterRuns
		if c.enterRecorded {
			return Entry{}, recorded(c.enterErr)
		}
		return Entry{}, c.enterErr
	}
	c.started = true
	return c.fakeEngine.Enter(ctx, cn, h, a)
}
func (c *chatEngine) Status(context.Context) (Status, error) {
	if c.statusErr != nil && c.enterCalls > 0 {
		return Status{}, c.statusErr
	}
	st := Status{Active: c.active || c.started, RunID: "p1/1"}
	if c.started && !c.otherRun {
		st.Hash = c.hash
	} else if c.started {
		st.Hash = "ffff0000"
	}
	return st, nil
}

// fakeChat is the ChatStarter: it records every call and fails at the step named in fail.
type fakeChat struct {
	log       *[]string
	fail      map[string]error
	budgets   []float64
	ended     []int64
	turns     []FirstTurn
	reused    bool
	makes     int
	goals     []string
	endCtxErr []error
}

func (f *fakeChat) Lock(context.Context) (func(), error) {
	if err := f.step("lock"); err != nil {
		return nil, err
	}
	return func() {}, nil
}
func (f *fakeChat) step(name string) error {
	*f.log = append(*f.log, name)
	return f.fail[name]
}
func (f *fakeChat) MakeBoss(_ context.Context, chat, goal string) (BossRef, error) {
	f.makes++
	f.goals = append(f.goals, goal)
	if err := f.step("make"); err != nil {
		return BossRef{}, err
	}
	return BossRef{SessionID: 7, ConversationID: "boss-conv", FrontDesk: chat, Reused: f.reused}, nil
}
func (f *fakeChat) SetBossBudget(_ context.Context, _ int64, usd float64) error {
	f.budgets = append(f.budgets, usd)
	return f.step("budget")
}
func (f *fakeChat) EndBoss(ctx context.Context, s int64) error {
	f.endCtxErr = append(f.endCtxErr, ctx.Err())
	f.ended = append(f.ended, s)
	return f.step("end")
}
func (f *fakeChat) QueueFirstTurn(_ context.Context, conv string, in FirstTurn) error {
	if conv != "boss-conv" {
		return errors.New("wrong conversation " + conv)
	}
	f.turns = append(f.turns, in)
	return f.step("turn")
}

type chatRig struct {
	*rig
	eng  *chatEngine
	chat *fakeChat
	log  []string
	file string
	hash string
}

func newChatRig(t *testing.T, charter string) *chatRig {
	t.Helper()
	c := &chatRig{rig: newRig()}
	c.eng = &chatEngine{log: &c.log}
	c.chat = &fakeChat{log: &c.log, fail: map[string]error{}}
	c.deps.Engine = func() Engine { return c.eng }
	c.deps.ChatStart = func() ChatStarter { return c.chat }
	c.file = writeCharter(t, charter)
	c.hash = fullHash(t, c.rig, c.file)
	c.terminal = false
	c.deps.JSONOutput = func() bool { return true }
	return c
}

func (c *chatRig) start(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"start", "--charter", c.file, "--gui-phrase", Phrase(0, c.hash), "--chat", "front-1"}, extra...)
	return run(t, c.deps, "", args...)
}

func (c *chatRig) steps() string { return strings.Join(c.log, ",") }

func TestChatStartRunsEveryStepInOrderAndQueuesTheFirstTurn(t *testing.T) {
	c := newChatRig(t, guiCharter)
	out, err := c.start(t)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if got := c.steps(); got != "lock,make,budget,check,enter,bind,turn" {
		t.Fatalf("steps = %s", got)
	}
	if len(c.chat.budgets) != 1 || c.chat.budgets[0] != 5 {
		t.Fatalf("a fixed charter budget must become the boss's ceiling: %v", c.chat.budgets)
	}
	if len(c.chat.turns) != 1 || c.chat.turns[0].RunID != "r1" || c.chat.turns[0].CharterHash != c.hash || c.chat.turns[0].Goal != "ship it" {
		t.Fatalf("first turn = %+v", c.chat.turns)
	}
	var doc struct {
		RunID     string `json:"run_id"`
		FirstTurn string `json:"first_turn"`
		Boss      int64  `json:"boss_session"`
	}
	_ = json.Unmarshal([]byte(lastJSON(out)), &doc)
	if doc.RunID != "r1" || doc.Boss != 7 || doc.FirstTurn != "queued" {
		t.Fatalf("answer = %s", out)
	}
	if len(c.chat.ended) != 0 {
		t.Fatal("a good start ended the boss")
	}
	if len(c.chat.goals) != 1 || c.chat.goals[0] != "ship it" {
		t.Fatalf("the boss carries the charter's goal: %v", c.chat.goals)
	}
}

const unlimitedGUICharter = `{"goal":"ship it","criteria":[{"kind":"rubric","text":"reads well"}],"limits":{"no_ceiling":true},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"}}`

func TestChatStartUnlimitedSetsNoCeilingOnTheBoss(t *testing.T) {
	c := newChatRig(t, unlimitedGUICharter)
	if out, err := c.start(t); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if len(c.chat.budgets) != 1 || c.chat.budgets[0] != 0 {
		t.Fatalf("unlimited must set the exact 'no budget' value (a reused boss may carry an old ceiling): %v", c.chat.budgets)
	}
}

// Each failure before the run exists undoes the boss and says nothing started; the run is never entered after a failed step.
func TestChatStartFailuresEndTheBossAndStartNothing(t *testing.T) {
	cases := []struct {
		name  string
		set   func(*chatRig)
		steps string
		ended bool
		text  string
	}{
		{"make", func(c *chatRig) { c.chat.fail["make"] = errors.New("no such chat") }, "lock,make", false, "no such chat"},
		{"budget", func(c *chatRig) { c.chat.fail["budget"] = errors.New("budget refused") }, "lock,make,budget,end", true, "budget refused"},
		{"check", func(c *chatRig) { c.eng.checkErr = errors.New("not eligible") }, "lock,make,budget,check,end", true, "not eligible"},
		{"enter", func(c *chatRig) { c.eng.enterErr = errors.New("the entry is refused") }, "lock,make,budget,check,enter,end", true, "the entry is refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChatRig(t, guiCharter)
			tc.set(c)
			out, err := c.start(t)
			if err == nil || !strings.Contains(err.Error()+out, tc.text) {
				t.Fatalf("err = %v %s", err, out)
			}
			if got := c.steps(); got != tc.steps {
				t.Fatalf("steps = %s, want %s", got, tc.steps)
			}
			if (len(c.chat.ended) == 1) != tc.ended || len(c.chat.turns) != 0 {
				t.Fatalf("ended=%v turns=%v", c.chat.ended, c.chat.turns)
			}
			if c.eng.started {
				t.Fatal("a run exists after a refused start")
			}
		})
	}
}

// The cleanup itself failing is said, never hidden: the person is told the boss may still stand.
func TestChatStartSaysWhenTheCleanupFailed(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.checkErr = errors.New("not eligible")
	c.chat.fail["end"] = errors.New("db locked")
	out, err := c.start(t)
	if err == nil || !strings.Contains(err.Error()+out, "not eligible") || !strings.Contains(err.Error()+out, "could not be ended") {
		t.Fatalf("err = %v %s", err, out)
	}
}

// A late failure of the entry with the run already recorded must not end the boss (a run with its boss gone is worse).
func TestChatStartKeepsTheBossWhenTheRunExistsAfterAFailedEntry(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.enterErr = errors.New("run p/1 started but its anchor could not be recorded (run `doomsday stop`)")
	c.eng.enterRuns, c.eng.enterRecorded = true, true
	_, err := c.start(t)
	if err == nil || !strings.Contains(err.Error(), " started but ") {
		t.Fatalf("err = %v", err)
	}
	if len(c.chat.ended) != 0 {
		t.Fatalf("the boss of a run that exists was ended: %v", c.chat.ended)
	}
}

func TestChatStartKeepsTheBossWhenItCannotBeRecorded(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.bindErr = errors.New("write failed")
	_, err := c.start(t)
	if err == nil || !strings.Contains(err.Error(), "started but") || len(c.chat.ended) != 0 || len(c.chat.turns) != 0 {
		t.Fatalf("err=%v ended=%v turns=%v", err, c.chat.ended, c.chat.turns)
	}
}

// The run started but the first turn did not queue: the start SUCCEEDS (a run exists) and the answer says so, visibly.
func TestChatStartReportsAFirstTurnThatDidNotQueue(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.chat.fail["turn"] = errors.New("host not running")
	out, err := c.start(t)
	if err != nil {
		t.Fatalf("a started run was reported as a failure: %v %s", err, out)
	}
	var doc struct {
		FirstTurn      string `json:"first_turn"`
		FirstTurnError string `json:"first_turn_error"`
		RunID          string `json:"run_id"`
	}
	if json.Unmarshal([]byte(lastJSON(out)), &doc) != nil || doc.FirstTurn != "failed" || !strings.Contains(doc.FirstTurnError, "host not running") || doc.RunID != "r1" {
		t.Fatalf("answer = %s", out)
	}
	if len(c.chat.ended) != 0 {
		t.Fatal("the boss of a started run was ended")
	}
}

// One run per profile: the second start is refused before anything is made, with the plain sentence.
func TestChatStartRefusesWhileARunExistsAndMakesNoBoss(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.active = true
	out, err := c.start(t)
	if err == nil || !strings.Contains(err.Error()+out, "a run already exists for this profile") {
		t.Fatalf("err = %v %s", err, out)
	}
	if c.steps() != "lock" || c.chat.makes != 0 {
		t.Fatalf("something was made: %s", c.steps())
	}
}

// Running the same start twice with the first one refused makes the same attempt again and ends it again: no pile of bosses.
func TestChatStartTwiceAfterARefusalIsIdempotent(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.checkErr = errors.New("not eligible")
	for i := 0; i < 2; i++ {
		if _, err := c.start(t); err == nil {
			t.Fatal("expected a refusal")
		}
	}
	if len(c.chat.ended) != 2 || c.chat.ended[0] != c.chat.ended[1] {
		t.Fatalf("ended = %v", c.chat.ended)
	}
	c.eng.checkErr = nil
	c.chat.reused = true // an unstamped boss left by a dead start is taken again, not duplicated
	if _, err := c.start(t); err != nil {
		t.Fatal(err)
	}
}

func TestChatStartFlagsAreExclusiveAndNeedTheGUIPath(t *testing.T) {
	c := newChatRig(t, guiCharter)
	if _, err := c.start(t, "--boss-session", "9"); err == nil || c.steps() != "" {
		t.Fatalf("--chat with --boss-session: %v %s", err, c.steps())
	}
	// no phrase from the app: --chat is the app's path only
	_, err := run(t, c.deps, "", "start", "--charter", c.file, "--yes-i-lift", "--hash", c.hash, "--chat", "front-1")
	if err == nil || c.steps() != "" {
		t.Fatalf("--chat without the app's phrase: %v %s", err, c.steps())
	}
}

func TestChatStartWithoutAChatStarterIsRefusedBeforeAnything(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.deps.ChatStart = nil
	if _, err := c.start(t); err == nil || len(c.eng.enters) != 0 {
		t.Fatalf("started without a chat starter: %v", err)
	}
}

// lastJSON is the last line of out that is a JSON object (the render for a person goes to the same stream in these tests).
func lastJSON(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "{") && json.Valid([]byte(l)) {
			return l
		}
	}
	return ""
}

// A failed Enter with a run that is NOT this start's (another process started one in between) ends our boss and says so.
func TestChatStartEndsTheBossWhenTheRunAfterAFailedEntryIsAnothers(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.enterErr = errors.New("the entry is refused")
	c.eng.enterRuns, c.eng.otherRun = true, true
	_, err := c.start(t)
	if err == nil || !strings.Contains(err.Error(), "a run already exists for this profile") || len(c.chat.ended) != 1 {
		t.Fatalf("err=%v ended=%v", err, c.chat.ended)
	}
}

// A Status that fails after a failed Enter is UNKNOWN: the boss is kept and the answer says the run may have started.
func TestChatStartTreatsAnUnreadableStatusAfterAFailedEntryAsUnknown(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.enterErr = errors.New("the entry is refused")
	c.eng.statusErr = errors.New("database is locked")
	_, err := c.start(t)
	if err == nil || !strings.Contains(err.Error(), " started but ") || !strings.Contains(err.Error(), "press Stop") || len(c.chat.ended) != 0 {
		t.Fatalf("err=%v ended=%v", err, c.chat.ended)
	}
}

// The cleanup runs on a context of its own: a cancelled CLI still ends the boss it made.
func TestChatStartCleanupSurvivesACancelledContext(t *testing.T) {
	c := newChatRig(t, guiCharter)
	ctx, cancel := context.WithCancel(context.Background())
	c.eng.checkErr = errors.New("not eligible")
	c.eng.onCheck = cancel
	cmd := NewCommand(c.deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"start", "--charter", c.file, "--gui-phrase", Phrase(0, c.hash), "--chat", "front-1"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("expected a refusal")
	}
	if len(c.chat.ended) != 1 || c.chat.endCtxErr[0] != nil {
		t.Fatalf("ended=%v ctx errors=%v", c.chat.ended, c.chat.endCtxErr)
	}
}

// A second start while one holds the profile's lock is refused as busy before anything is read or made.
func TestChatStartRefusesWhileAnotherStartHoldsTheLock(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.chat.fail["lock"] = daemonhb.ErrHeld
	_, err := c.start(t)
	if err == nil || !strings.Contains(err.Error(), "a start is already in progress") || c.steps() != "lock" || c.chat.makes != 0 {
		t.Fatalf("err=%v steps=%s", err, c.steps())
	}
}

// A terminal start of the very same charter that wins the race makes our Enter fail BEFORE it records anything: the run that
// exists is its, not ours, even though its hash is the same. Our boss is ended.
func TestChatStartEndsTheBossWhenARunOfTheSameCharterIsAnothersStart(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.enterErr = errors.New("doomsday: generation moved")
	c.eng.enterRuns = true // a run of this very charter exists (Status carries our hash)
	_, err := c.start(t)
	if err == nil || !strings.Contains(err.Error(), "a run already exists for this profile") || len(c.chat.ended) != 1 {
		t.Fatalf("err=%v ended=%v", err, c.chat.ended)
	}
}

// The marker survives wrapping: only a failure after the record is "ours".
func TestErrRunRecordedMarksOnlyFailuresAfterTheRecord(t *testing.T) {
	if !errors.Is(fmt.Errorf("x: %w", recorded(errors.New("y"))), ErrRunRecorded) || errors.Is(errors.New("y"), ErrRunRecorded) {
		t.Fatal("the marker is wrong")
	}
}
