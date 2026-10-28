package coderhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A session with no master has nobody to ask but the person. Only a hard gap is parked for them;
// every other gap is refused so the model decides and records it, or stops.

func soloStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	st, clk := parkStore(t)
	addConversation(t, st, "solo", "/w/solo")
	return st, clk
}

func TestNoMasterParksOnlyHardGapsForThePerson(t *testing.T) {
	for _, preset := range []string{"ask-on-risk", "decide-and-log", "ask-always"} {
		for _, class := range []string{"ambiguity", "scope_growth", "verification_failed", "child_conflict"} {
			for _, rev := range []bool{false, true} {
				st, _ := soloStore(t)
				setSettings(t, st, "solo", `{"autonomy_preset": "`+preset+`"}`)
				cmds := pendingCount(t, st)
				_, err := st.ParkOwn(caller("solo"), ParkRequest{Class: class, Question: "which?", Default: "a", Reversible: rev})
				if !errors.Is(err, ErrNoMasterDecide) {
					t.Fatalf("%s %s rev=%v = %v, want ErrNoMasterDecide", preset, class, rev, err)
				}
				if st.OpenParked("p", "solo") != 0 || pendingCount(t, st) != cmds {
					t.Fatalf("%s %s rev=%v: a refused park left a question or a command", preset, class, rev)
				}
				if x, _, _ := st.GetSession("p", "solo"); x.State == StateWaitingUser {
					t.Fatal("a refused park put the session in waiting_user")
				}
			}
		}
	}
	if !strings.Contains(ErrNoMasterDecide.Error(), "no master to answer: decide and call record, or stop") {
		t.Fatalf("text = %q", ErrNoMasterDecide)
	}
}

func TestNoMasterRiskyParkWaitsForThePersonWithNoTimeout(t *testing.T) {
	for _, class := range []string{"risky_action", "missing_credential"} {
		st, clk := soloStore(t)
		decisions := func() int { l, _ := st.ListDecisions("p", "solo", LedgerFilter{}); return len(l) }
		cmds := pendingCount(t, st)
		q, err := st.ParkOwn(caller("solo"), ParkRequest{Class: class, Question: "force push?", Default: "yes do it"})
		if err != nil || q.Answerer != WhoUser || q.Status != "open" {
			t.Fatalf("%s park = %+v %v", class, q, err)
		}
		if x, _, _ := st.GetSession("p", "solo"); x.State != StateWaitingUser {
			t.Fatalf("state = %s", x.State)
		}
		if n := st.OpenParked("p", "solo"); n != 1 {
			t.Fatalf("open = %d", n)
		}
		before := protectedState(t, st.db) // no approval, HIL or workflow row appears with it
		setSettings(t, st, "solo", `{"autonomy_preset": "decide-and-log"}`)
		pass := func(label string, wantErr bool) {
			for i := 1; i <= 100; i++ {
				clk.t = clk.t.Add(time.Duration(i) * ParkTimeout)
				if n, err := st.ApplyDefaults(); n != 0 || (err != nil) != wantErr {
					t.Fatalf("%s %s pass %d applied %d (%v)", class, label, i, n, err)
				}
			}
		}
		pass("valid settings", false)
		// a stored policy that tries to hand the gap to the model with a default changes nothing
		if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = ? WHERE conversation_id = 'solo'`,
			`{"autonomy_classes":{"`+class+`":{"who":"model","fallback":"take_default"}}}`); err != nil {
			t.Fatal(err)
		}
		pass("stored take_default", true)
		if got, _ := st.GetQuestion("p", q.ID); got.Status != "open" || got.Answer != "" {
			t.Fatalf("%s: question = %+v", class, got)
		}
		if decisions() != 0 || pendingCount(t, st) != cmds {
			t.Fatalf("%s: a timeout wrote a ledger row or queued a note", class)
		}
		if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = '{}' WHERE conversation_id = 'solo'`); err != nil {
			t.Fatal(err)
		}
		// only the person answers, once: one sealed host-made note
		if _, _, err := st.Answer("p", q.ID, "no", masterScope{conv: "m"}); !errors.Is(err, ErrNotAnswerer) {
			t.Fatalf("a master answered a masterless question: %v", err)
		}
		_, cmd, err := st.Answer("p", q.ID, "no", OperatorScope{})
		if err != nil || !strings.HasPrefix(cmd.TurnID, AnswerPrefix+answerFromPerson+"-") || cmd.Issuer != "" {
			t.Fatalf("answer = %+v %v", cmd, err)
		}
		if _, _, err := st.Answer("p", q.ID, "again", OperatorScope{}); !errors.Is(err, ErrNotOpen) {
			t.Fatalf("second answer = %v", err)
		}
		if n := len(answerCommands(t, st, "solo")); n != 1 {
			t.Fatalf("%d answer notes", n)
		}
		after := protectedState(t, st.db)
		delete(before, "coder_sessions(protected)") // the test itself changed the settings
		delete(after, "coder_sessions(protected)")
		diffProtected(t, class+" park, timeouts and answer", before, after)
		if !strings.Contains(cmd.Message, "grants no access") || cmd.WantsMission {
			t.Fatalf("the note is not sealed data: %+v", cmd)
		}
	}
}

func TestParkWithAMasterIsUnchanged(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "ambiguity", Question: "drop the column?"})
	if err != nil || q.Answerer != WhoMaster {
		t.Fatalf("park under a master = %+v %v", q, err)
	}
	setSettings(t, st, "k1", `{"autonomy_preset": "ask-always"}`)
	if q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "scope_growth", Question: "wider?"}); err != nil || q.Answerer != WhoUser {
		t.Fatalf("ask-always = %+v %v", q, err)
	}
	// a master with no master of its own is masterless too
	if _, err := st.ParkOwn(caller("m"), ParkRequest{Class: "ambiguity", Question: "q"}); !errors.Is(err, ErrNoMasterDecide) {
		t.Fatalf("a master parking = %v", err)
	}
}

func TestOrphanedChildIsMasterless(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	if _, err := st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = '' WHERE conversation_id = 'k1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "ambiguity", Question: "q"}); !errors.Is(err, ErrNoMasterDecide) {
		t.Fatalf("orphan = %v", err)
	}
	if q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "risky_action", Question: "q"}); err != nil || q.Answerer != WhoUser {
		t.Fatalf("orphan risky = %+v %v", q, err)
	}
}

func itemCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_person_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A risky park with no master makes exactly one item; a refused park, a park under a master and a
// timeout make none that the person must act on and answer none; the item is open only while its
// question is, and the answer is delivered once whichever way it came.
func TestNoMasterRiskyParkMakesOneItemAnsweredOnce(t *testing.T) {
	st, clk := soloStore(t)
	if _, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "ambiguity", Question: "q"}); !errors.Is(err, ErrNoMasterDecide) || itemCount(t, st) != 0 {
		t.Fatalf("refused park = %v, items %d", err, itemCount(t, st))
	}
	q, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "force push?"})
	if err != nil || itemCount(t, st) != 1 {
		t.Fatalf("risky park = %v, items %d", err, itemCount(t, st))
	}
	clk.t = clk.t.Add(100 * ParkTimeout)
	_, _ = st.ApplyDefaults()
	items, err := st.OpenPersonItems("p")
	if err != nil || len(items) != 1 || items[0].Kind != ItemParkedQuestion || items[0].RefID != q.ID || items[0].ConversationID != "solo" {
		t.Fatalf("items = %+v %v", items, err)
	}
	if other, _ := st.OpenPersonItems("other"); len(other) != 0 {
		t.Fatal("another profile sees the item")
	}
	_, cmd, err := st.Answer("p", q.ID, "no", OperatorScope{})
	if err != nil || !strings.HasPrefix(cmd.TurnID, AnswerPrefix+answerFromPerson+"-") || cmd.Issuer != "" || cmd.WantsMission {
		t.Fatalf("answer = %+v %v", cmd, err)
	}
	if _, _, err := st.Answer("p", q.ID, "again", OperatorScope{}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("second answer = %v", err)
	}
	if n := len(answerCommands(t, st, "solo")); n != 1 {
		t.Fatalf("%d notes", n)
	}
	if left, _ := st.OpenPersonItems("p"); len(left) != 0 {
		t.Fatalf("answered item still open: %+v", left)
	}
	if err := st.addPersonItem("p", "solo", "budget_request", "risky_action", 1); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}
