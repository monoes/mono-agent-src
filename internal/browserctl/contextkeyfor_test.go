package browserctl

import "testing"

// ContextKeyFor keeps the session-state file names the old ContextKey gave: the
// state a caller built up before the turn-aware policy is still found.
func TestContextKeyForKeepsTheOldKeys(t *testing.T) {
	actAsHuman(t)
	for _, tc := range []struct {
		name string
		c    Caller
		want string
	}{
		{"the user's terminal", Caller{}, "human"},
		{"the chat", Caller{Kind: KindChat}, "chat"},
		{"another agent", Caller{Kind: KindOther}, "agent"},
		{"an unproven claim", Caller{Kind: KindOther, Declared: KindCoder}, "agent"},
		{"a chat marker inside a turn", Caller{Kind: KindOther, Declared: KindChat}, "agent"},
		{"a coder", Caller{Kind: KindCoder}, "agent"},
	} {
		if got := ContextKeyFor(tc.c); got != tc.want {
			t.Errorf("%s: ContextKeyFor = %q, want %q", tc.name, got, tc.want)
		}
	}

	t.Setenv("MONOAGENT_CONVERSATION_ID", "c-42")
	for _, c := range []Caller{{}, {Kind: KindChat}, {Kind: KindOther}, {Kind: KindCoder}} {
		if got := ContextKeyFor(c); got != "conv-c-42" {
			t.Errorf("a coder conversation, caller %+v: ContextKeyFor = %q, want conv-c-42", c, got)
		}
	}
}

// LevelOf is the stricter of a caller's kind and the kind it declared: a chat
// marker inside a turn binds at the stricter of chat and other, whichever
// way round the user set them.
func TestLevelOfIsTheStricterOfTheTwoKinds(t *testing.T) {
	inTurnChat := Caller{Kind: KindOther, Declared: KindChat}
	for _, tc := range []struct{ chat, other, want string }{
		{LevelOff, LevelFullNoApprovals, LevelOff},
		{LevelFullNoApprovals, LevelOff, LevelOff},
		{LevelFull, LevelAsk, LevelAsk},
		{LevelDev, LevelFull, LevelDev},
	} {
		p := Permissions{Levels: map[string]string{KindChat: tc.chat, KindOther: tc.other}}
		if got := p.LevelOf(inTurnChat); got != tc.want {
			t.Errorf("chat %s, other %s: LevelOf = %s, want %s", tc.chat, tc.other, got, tc.want)
		}
	}
	// Outside a turn the chat keeps its own level.
	p := Permissions{Levels: map[string]string{KindChat: LevelFullNoApprovals, KindOther: LevelOff}}
	if got := p.LevelOf(Caller{Kind: KindChat}); got != LevelFullNoApprovals {
		t.Errorf("the chat outside a turn: LevelOf = %s", got)
	}
}
