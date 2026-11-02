package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// askFixture puts the lead m.master (mission m.se) under mid (chat m.a) under the other lead (m.other): the
// grandparent of the master's mission is the other lead's.
func newAskFixture(t *testing.T) (*missionChildFixture, tasks.Session) {
	t.Helper()
	m := newMissionChildFixture(t, true)
	mid, err := m.f.Store.CreateSession(opCtx, m.profile, tasks.SessionInput{Title: "mid", ConversationID: m.a, ParentSessionID: m.otherSe.ID, Kind: tasks.SessionKindDirection}, sessOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.f.Side.DB.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, mid.ID, m.se.ID); err != nil {
		t.Fatal(err)
	}
	return m, mid
}

func commandsFor(t *testing.T, m *missionChildFixture, conv string) int {
	t.Helper()
	var n int
	if err := m.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ?`, conv).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAskAncestorToolIsServedAndGated(t *testing.T) {
	n := 0
	for _, tl := range allTools() {
		if strings.Contains(tl.name, "ancestor") {
			n++
			if tl.name != "session_ask_ancestor" || !tl.mutating {
				t.Errorf("%s: mutating=%v", tl.name, tl.mutating)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d ancestor tools: the person's answer must have no tool", n)
	}
}

func TestLeadAsksItsGrandparentAndAHelperCannot(t *testing.T) {
	m, _ := newAskFixture(t)
	for _, role := range []string{agenttokens.RoleWorker, agenttokens.RoleVerifier} {
		if _, err := m.asBound(t, role, "session_ask_ancestor", map[string]any{"question": "q"}); err == nil || !strings.Contains(err.Error(), "not_yours") {
			t.Fatalf("%s: a bound helper asked: %v", role, err)
		}
	}
	if _, err := m.as(t, m.tokA, "session_ask_ancestor", map[string]any{"question": "q"}); err == nil || !strings.Contains(err.Error(), "not_yours") {
		t.Fatalf("a session that leads nothing asked: %v", err)
	}
	if _, err := m.as(t, "", "session_ask_ancestor", map[string]any{"question": "q"}); err == nil || !strings.Contains(err.Error(), "no_session") {
		t.Fatalf("no token: %v", err)
	}
	if n := commandsFor(t, m, m.other); n != 0 {
		t.Fatalf("refused asks queued %d", n)
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, err := m.as(t, m.tokM, "session_ask_ancestor", map[string]any{"question": "what is the budget?"})
	if err != nil || !strings.Contains(out, `"delivery": "lead"`) {
		t.Fatalf("%v %s", err, out)
	}
	if n := commandsFor(t, m, m.other); n != 1 {
		t.Fatalf("the ancestor lead got %d", n)
	}
	// the parent's chat is never written to, and a direct-parent target is refused with its own reason
	if n := commandsFor(t, m, m.a); n != 0 {
		t.Fatalf("the parent got %d", n)
	}
	if _, err := m.as(t, m.tokM, "session_ask_ancestor", map[string]any{"question": "q", "level": itoaI(m.se.ID)}); err == nil {
		t.Fatal("asked itself")
	}
}

func TestACriticalAskFromTheToolRaisesAPersonItem(t *testing.T) {
	m, _ := newAskFixture(t)
	out, err := m.as(t, m.tokM, "session_ask_ancestor", map[string]any{"question": "q", "importance": "critical"})
	if err != nil || !strings.Contains(out, `"delivery": "person"`) {
		t.Fatalf("%v %s", err, out)
	}
	items, err := m.store.OpenPersonItems(m.profile)
	if err != nil || len(items) != 1 || items[0].Kind != coderhost.ItemAncestorQuestion {
		t.Fatalf("%+v %v", items, err)
	}
	if n := commandsFor(t, m, m.other); n != 0 {
		t.Fatalf("a critical ask reached a model chat: %d", n)
	}
}

func TestAskToolSourceNamesNoAnswerOrApprovalWriter(t *testing.T) {
	forbidden := map[string]bool{"AnswerAncestorQuestion": true, "ApproveSession": true, "SaveSettings": true, "SetBudget": true, "Enqueue": true,
		"addPersonItem": true, "RaisePersonItem": true, "Record": true}
	f, err := parser.ParseFile(token.NewFileSet(), "ask_ancestor_tool.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if x, ok := n.(*ast.SelectorExpr); ok && forbidden[x.Sel.Name] {
			t.Errorf("ask_ancestor_tool.go uses %s", x.Sel.Name)
		}
		return true
	})
}

func itoaI(n int64) string { return strconv.FormatInt(n, 10) }

// The description tells the asking lead how its ask is answered: by the ancestor's session_reply_ask,
// never by session_send (which is always an ordinary command to a session below).
func TestAskAncestorDescriptionNamesTheReplyTool(t *testing.T) {
	for _, tl := range allTools() {
		if tl.name != "session_ask_ancestor" {
			continue
		}
		if !strings.Contains(tl.description, "session_reply_ask") {
			t.Errorf("the description does not name session_reply_ask: %s", tl.description)
		}
		if strings.Contains(tl.description, "answers with session_send") || strings.Contains(tl.description, "its session_send to you is that reply") {
			t.Errorf("the description says session_send answers an ask: %s", tl.description)
		}
		return
	}
	t.Fatal("session_ask_ancestor is not served")
}
