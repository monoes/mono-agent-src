package mcp

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

func TestSessionParkAndRecordActOnTheCallersOwnSession(t *testing.T) {
	sf := newSessionFixture(t)
	var cmds int
	sf.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands`).Scan(&cmds)

	out, err := sf.as(t, sf.tokA, "session_park", map[string]any{"class": "risky_action", "question": "force push?", "options": []string{"yes", "no"}})
	if err != nil || !strings.Contains(out, "questionId") || !strings.Contains(out, `"master"`) {
		t.Fatalf("park = %v %s", err, out)
	}
	if sf.store.OpenParked(sf.profile, sf.a) != 1 || sf.store.OpenParked(sf.profile, sf.b) != 0 || sf.store.OpenParked(sf.profile, sf.master) != 0 {
		t.Fatal("the question is not on the caller's session alone")
	}
	// there is no argument to name another session: an extra one is refused
	if _, err := sf.as(t, sf.tokA, "session_park", map[string]any{"session": sf.b, "class": "risky_action", "question": "q"}); err == nil {
		t.Fatal("a session argument was accepted")
	}
	if _, err := sf.as(t, sf.tokA, "session_park", map[string]any{"class": "ambiguity", "question": "tabs?", "reversible": true}); err == nil || !strings.Contains(err.Error(), "model_decides") || !strings.Contains(err.Error(), "session_record") {
		t.Fatalf("model-decided park = %v", err)
	}
	for _, args := range []map[string]any{
		{"class": "risky_action", "question": "q", "reversible": true},
		{"class": "nonsense", "question": "q"},
	} {
		if _, err := sf.as(t, sf.tokA, "session_park", args); err == nil || !strings.Contains(err.Error(), "invalid_input") {
			t.Errorf("park %v = %v", args, err)
		}
	}
	for i := 1; i < coderhost.MaxOpenQuestions; i++ {
		if _, err := sf.as(t, sf.tokA, "session_park", map[string]any{"class": "risky_action", "question": "q"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sf.as(t, sf.tokA, "session_park", map[string]any{"class": "risky_action", "question": "q"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("over the cap = %v", err)
	}

	out, err = sf.as(t, sf.tokA, "session_record", map[string]any{"class": "ambiguity", "question": "tabs?", "choice": "tabs", "alternatives": []string{"spaces"}, "reason": "repo style", "reversible": true})
	if err != nil || !strings.Contains(out, `"model"`) {
		t.Fatalf("record = %v %s", err, out)
	}
	for _, args := range []map[string]any{
		{"class": "risky_action", "choice": "go"},
		{"class": "missing_credential", "choice": "guess", "reversible": true},
		{"class": "ambiguity", "choice": "irreversible one"},
	} {
		if _, err := sf.as(t, sf.tokA, "session_record", args); err == nil || !strings.Contains(err.Error(), "invalid_input") {
			t.Errorf("record %v = %v", args, err)
		}
	}
	if l, _ := sf.store.ListDecisions(sf.profile, sf.a, coderhost.LedgerFilter{}); len(l) != 1 || l[0].MadeBy != "model" {
		t.Fatalf("ledger = %+v", l)
	}
	if l, _ := sf.store.ListDecisions(sf.profile, sf.b, coderhost.LedgerFilter{}); len(l) != 0 {
		t.Fatal("recorded on a peer")
	}
	for _, tok := range []string{"", "mst-forged"} {
		if _, err := sf.as(t, tok, "session_record", map[string]any{"class": "ambiguity", "choice": "c", "reversible": true}); err == nil || !strings.Contains(err.Error(), "no_session") {
			t.Errorf("token %q: %v", tok, err)
		}
	}
	var after int
	sf.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands`).Scan(&after)
	if after != cmds {
		t.Fatalf("park or record queued %d commands", after-cmds)
	}
}

func TestSessionRecordIsRateLimitedPerSession(t *testing.T) {
	sf := newSessionFixture(t)
	args := map[string]any{"class": "ambiguity", "choice": "c", "reversible": true}
	for i := 0; i < coderhost.MaxRecordsPerHour; i++ {
		if _, err := sf.as(t, sf.tokA, "session_record", args); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if _, err := sf.as(t, sf.tokA, "session_record", args); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("over the rate = %v", err)
	}
}
