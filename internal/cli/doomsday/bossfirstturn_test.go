package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// #1745: `start --boss-session N` bound the boss but queued no first turn, so the boss could never act and a steer was
// refused. The start now queues the first turn the way the start from a chat does.

// bossConvChat is a fakeChat that also knows the conversation of a boss session (the lookup `start --boss-session` needs).
type bossConvChat struct {
	*fakeChat
	convErr error
	asked   []int64
}

func (b *bossConvChat) BossConversation(_ context.Context, session int64) (string, error) {
	b.asked = append(b.asked, session)
	return "boss-conv", b.convErr
}

func bossStartRig(t *testing.T, asJSON bool) (*rig, *bossConvChat, []string) {
	t.Helper()
	r := newRig()
	log := []string{}
	cc := &bossConvChat{fakeChat: &fakeChat{log: &log, fail: map[string]error{}}}
	r.deps.Engine = func() Engine { return &bossEngine{fakeEngine: r.e} }
	r.deps.ChatStart = func() ChatStarter { return cc }
	file := writeCharter(t, goodCharter)
	h := fullHash(t, r, file)
	r.deps.JSONOutput = func() bool { return asJSON }
	return r, cc, []string{"start", "--charter", file, "--yes-i-lift", "--hash", h, "--boss-session", "7"}
}

func TestStartBossSessionQueuesTheBossFirstTurn(t *testing.T) {
	r, cc, args := bossStartRig(t, false)
	out, err := run(t, r.deps, "", args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if len(cc.asked) != 1 || cc.asked[0] != 7 {
		t.Fatalf("the boss's conversation was not looked up for session 7: %v", cc.asked)
	}
	if len(cc.turns) != 1 || cc.turns[0].RunID == "" || cc.turns[0].CharterHash == "" || cc.turns[0].Goal == "" {
		t.Fatalf("first turn = %+v", cc.turns)
	}
	if !strings.Contains(out, "first turn: queued") {
		t.Fatalf("the start must say the first turn is queued: %s", out)
	}
}

func TestStartBossSessionSaysWhenTheFirstTurnFailed(t *testing.T) {
	r, cc, args := bossStartRig(t, false)
	cc.fail["turn"] = errors.New("no run without binding: x")
	out, err := run(t, r.deps, "", args...)
	if err != nil { // the run started: a failed first turn is not a failed start (as for the start from a chat)
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(out, "the boss has no first turn: no run without binding: x") {
		t.Fatalf("out = %s", out)
	}
}

func TestStartBossSessionJSONCarriesTheFirstTurn(t *testing.T) {
	r, _, args := bossStartRig(t, true)
	out, err := run(t, r.deps, "", args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var doc struct {
		RunID            string `json:"run_id"`
		FirstTurn        string `json:"first_turn"`
		BossSession      int64  `json:"boss_session"`
		BossConversation string `json:"boss_conversation"`
	}
	if err := json.Unmarshal([]byte(lastJSON(out)), &doc); err != nil || doc.RunID == "" || doc.FirstTurn != "queued" || doc.BossSession != 7 || doc.BossConversation != "boss-conv" {
		t.Fatalf("answer = %s (%v)", out, err)
	}
}

// The status hint told the person to type in the chat; that turn runs in the front-desk chat, outside the run.
func TestStatusNoFirstTurnHintDoesNotSendThePersonToTheChat(t *testing.T) {
	r := newRig()
	r.deps.FirstTurn = func(context.Context) (FirstTurnState, error) { return FirstTurnState{State: "none"}, nil }
	out, err := run(t, r.deps, "", "status")
	if err != nil || !strings.Contains(out, "first turn: not queued") {
		t.Fatalf("%q %v", out, err)
	}
	if strings.Contains(out, "type in its chat") {
		t.Fatalf("the hint still sends the person to a chat that cannot reach the boss: %s", out)
	}
}
