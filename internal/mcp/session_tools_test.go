package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// sessionFixture is a master with two children in the default profile and the tokens of their turns.
type sessionFixture struct {
	f              *taskFixture
	store          *coderhost.Store
	profile        string
	master, a, b   string
	tokM, tokA     string
	callAsAnything func(string, map[string]any) (string, error)
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	f := newTaskFixture(t, taskSetup{})
	t.Setenv(coderhost.SessionTokenEnv, "")
	sf := &sessionFixture{f: f, store: coderhost.NewStore(f.Side.DB)}
	if err := f.Side.DB.QueryRow(`SELECT id FROM profiles WHERE id = ? OR name = ? LIMIT 1`, serverTestProfile(f.Server), serverTestProfile(f.Server)).Scan(&sf.profile); err != nil {
		t.Fatal(err)
	}
	store, _ := ai.NewAIStore(f.Side.DB)
	conv, err := store.CreateConversationAccess(sf.profile, "agent", "", "claude", "", "m", "workspace", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	sf.master = conv.ID
	sf.store.EnsureSession(sf.profile, sf.master)
	if err := sf.store.SetRole(sf.profile, sf.master, "master", ""); err != nil {
		t.Fatal(err)
	}
	mc := coderhost.Caller{ProfileID: sf.profile, ConversationID: sf.master}
	for _, dst := range []*string{&sf.a, &sf.b} {
		out, err := sf.store.Spawn(mc, coderhost.SpawnRequest{})
		if err != nil {
			t.Fatal(err)
		}
		*dst = out.ConversationID
	}
	sf.tokM = sf.run(t, sf.master, "tm")
	sf.tokA = sf.run(t, sf.a, "ta")
	return sf
}

// run marks a session as running a turn, as the host does, and mints its token.
func (sf *sessionFixture) run(t *testing.T, conv, turn string) string {
	t.Helper()
	x, _, _ := sf.store.GetSession(sf.profile, conv)
	x.State, x.TurnID = coderhost.StateRunning, turn
	if err := sf.store.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	tok, err := sf.store.IssueSessionToken(sf.profile, conv, turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.store.BindSessionToken(sf.profile, conv, turn, os.Getpid(), coderhost.ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	return tok
}

// as calls a tool in the process of a turn that holds tok.
func (sf *sessionFixture) as(t *testing.T, tok, name string, args map[string]any) (string, error) {
	t.Helper()
	t.Setenv(coderhost.SessionTokenEnv, tok)
	if args == nil {
		args = map[string]any{}
	}
	b, _ := json.Marshal(args)
	return callToolIn(context.Background(), sf.f.Server, sf.profile, name, b)
}

func TestSessionToolsAreServedAndGated(t *testing.T) {
	names := map[string]tool{}
	for _, tl := range sessionTools() {
		names[tl.name] = tl
	}
	for _, n := range []string{"session_spawn", "session_list", "session_status", "session_send", "session_pause", "session_resume", "session_stop", "session_set"} {
		tl, ok := names[n]
		if !ok {
			t.Fatalf("%s is not defined", n)
		}
		readOnly := n == "session_list" || n == "session_status"
		if tl.mutating == readOnly {
			t.Errorf("%s mutating = %v", n, tl.mutating)
		}
	}
	if len(names) != 8 {
		t.Fatalf("tools = %d", len(names))
	}
	found := 0
	for _, tl := range allTools() {
		if strings.HasPrefix(tl.name, "session_") {
			found++
		}
	}
	if found != 8 {
		t.Fatalf("allTools serves %d session tools", found)
	}
}

func TestMasterToolsWorkOnDescendantsAndRefusePeersForgeriesAndOperatorSettings(t *testing.T) {
	sf := newSessionFixture(t)
	out, err := sf.as(t, sf.tokM, "session_list", nil)
	if err != nil || !strings.Contains(out, sf.a) || !strings.Contains(out, sf.b) {
		t.Fatalf("list = %v %s", err, out)
	}
	if out, err := sf.as(t, sf.tokM, "session_send", map[string]any{"session": sf.a, "message": "go"}); err != nil || !strings.Contains(out, "master-") {
		t.Fatalf("send = %v %s", err, out)
	}
	if _, err := sf.as(t, sf.tokM, "session_pause", map[string]any{"session": sf.b}); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.as(t, sf.tokM, "session_set", map[string]any{"session": sf.a, "patch": map[string]any{"checkin_minutes": 7}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.as(t, sf.tokM, "session_set", map[string]any{"session": sf.a, "patch": map[string]any{"max_children": 9}}); err == nil || !strings.Contains(err.Error(), "operator_only") {
		t.Fatalf("operator-only setting: %v", err)
	}
	if out, err := sf.as(t, sf.tokM, "session_spawn", map[string]any{"access": "full"}); err == nil || !strings.Contains(err.Error(), "access") {
		t.Fatalf("full spawn: %v %s", err, out)
	}
	if out, err := sf.as(t, sf.tokM, "session_spawn", map[string]any{}); err != nil || !strings.Contains(out, "conversationId") {
		t.Fatalf("spawn = %v %s", err, out)
	}

	// a child's token is a peer's: it reads itself and nobody else, and controls nothing
	if _, err := sf.as(t, sf.tokA, "session_status", map[string]any{"session": sf.a}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"session_status", map[string]any{"session": sf.b}},
		{"session_send", map[string]any{"session": sf.b, "message": "x"}},
		{"session_stop", map[string]any{"session": sf.b}},
		{"session_send", map[string]any{"session": sf.master, "message": "x"}},
		{"session_send", map[string]any{"session": sf.a, "message": "x"}},
		{"session_set", map[string]any{"session": sf.b, "patch": map[string]any{"checkin_minutes": 9}}},
	} {
		if _, err := sf.as(t, sf.tokA, c.tool, c.args); err == nil || !strings.Contains(err.Error(), "not_yours") {
			t.Errorf("%s %v from a child: %v", c.tool, c.args, err)
		}
	}

	// no token, a forged one, a token for another profile: no control
	for _, tok := range []string{"", "mst-forged", sf.tokA + "x"} {
		if _, err := sf.as(t, tok, "session_send", map[string]any{"session": sf.a, "message": "x"}); err == nil || !strings.Contains(err.Error(), "no_session") {
			t.Errorf("token %q: %v", tok, err)
		}
	}
	t.Setenv(coderhost.SessionTokenEnv, sf.tokM)
	if _, err := callToolIn(context.Background(), sf.f.Server, "other", "session_list", json.RawMessage(`{}`)); err == nil {
		t.Error("a token worked for another profile")
	}
	// a send never queues a mission or a person's turn id
	var n int
	sf.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE kind = 'send' AND turn_id NOT LIKE 'master-%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d sends without the master- prefix", n)
	}
}
