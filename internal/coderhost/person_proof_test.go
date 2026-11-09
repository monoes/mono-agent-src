package coderhost

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// testProof is the proof of a plain operator, as the person gate hands it out.
var testProof = func() PersonProof {
	p, err := ProvePerson(tasks.Actor{Kind: tasks.Human})
	if err != nil {
		panic(err)
	}
	return p
}()

func TestProvePersonRefusesEveryActorThatIsNotThePerson(t *testing.T) {
	for name, a := range map[string]tasks.Actor{
		"zero":       {},
		"agent":      {Kind: tasks.Agent, Name: "w"},
		"capture":    {Kind: tasks.Capture},
		"bound sess": {Kind: tasks.Human, Session: 3},
		"bound task": {Kind: tasks.Human, Task: 3},
		"verifier":   {Kind: tasks.Human, Verifier: true},
		"chat":       {Kind: tasks.Human, Conversation: "c"},
		"planner":    {Kind: tasks.Human, MissionPlanner: true},
	} {
		if _, err := ProvePerson(a); err == nil {
			t.Errorf("%s: ProvePerson accepted it", name)
		}
	}
	if _, err := ProvePerson(tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatalf("the plain operator is refused: %v", err)
	}
}

func TestThePersonHelpersRefuseTheZeroProof(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.AnswerAncestorQuestion(PersonProof{}, "p", got.AskID, "x"); err == nil {
		t.Error("AnswerAncestorQuestion ran without a proof")
	}
	if _, err := f.st.TellLeadAboutPlan(bg, PersonProof{}, "p", f.cc, 7, PlanAsked, "x"); err == nil {
		t.Error("TellLeadAboutPlan ran without a proof")
	}
	if _, err := f.st.DenyPlan(bg, PersonProof{}, PersonItem{ProfileID: "p", ConversationID: f.cc, RefID: 7}, "you", "x"); err == nil {
		t.Error("DenyPlan ran without a proof")
	}
	if n := len(f.commandsTo(f.cc)); n != 0 {
		t.Errorf("%d commands were queued without a proof", n)
	}
}

// A caller that is not a person path cannot queue a reserved turn id, by any entry that writes the queue.
func TestTheReservedPersonTurnIdsAreRefusedWithoutThePersonPath(t *testing.T) {
	f := newAskTree(t)
	for _, turn := range []string{"answer-p-abc", "ask-p-abc", "answer-p-deny-1-1", "ask-p-"} {
		c := Command{ProfileID: "p", ConversationID: f.cc, Kind: KindSend, TurnID: turn, Message: "hi"}
		if _, err := f.st.Enqueue(c); err == nil || !strings.Contains(err.Error(), "person's own answers") {
			t.Errorf("Enqueue(%s) = %v, want the reserved-prefix refusal", turn, err)
		}
		if _, _, err := f.st.EnqueueSendBounded(c, SendBounds{}); err == nil {
			t.Errorf("EnqueueSendBounded(%s) = %v", turn, err)
		}
		c.Issuer = f.rc
		if _, err := f.st.Enqueue(c); err == nil || !strings.Contains(err.Error(), "person's own answers") {
			t.Errorf("Enqueue(%s) with an issuer = %v", turn, err)
		}
	}
	if n := len(f.commandsTo(f.cc)); n != 0 {
		t.Errorf("%d commands were queued", n)
	}
	// the neighbours are not reserved
	for _, turn := range []string{"answer-m-abc", "answer-d-abc", "ask-q-abc", "t-answer-p-x"} {
		if reservedPersonTurn(turn) {
			t.Errorf("%s is wrongly reserved", turn)
		}
	}
}

// The person's flows keep working: a plan question, a critical-ask answer and a parked-question answer each queue
// their reserved turn id, and the pursuit loop's definition still reads them as the person.
func TestThePersonPathsStillQueueTheirReservedTurnIds(t *testing.T) {
	f := newAskTree(t)
	cmd, err := f.st.TellLeadAboutPlan(bg, testProof, "p", f.cc, 7, PlanAsked, "which?")
	if err != nil || !strings.HasPrefix(cmd.TurnID, "answer-p-") || !reservedPersonTurn(cmd.TurnID) {
		t.Fatalf("plan question: %+v, %v", cmd, err)
	}
	got, err := f.st.AskAncestor(f.lead(f.gc), "p", AskRequest{Question: "q", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	_, cmd, err = f.st.AnswerAncestorQuestion(testProof, "p", got.AskID, "yes")
	if err != nil || !strings.HasPrefix(cmd.TurnID, "ask-p-") {
		t.Fatalf("ask answer: %+v, %v", cmd, err)
	}
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q, _ := st.Park("p", "a", "risky_action", "wipe?", nil, "", false)
	if _, cmd, err = st.Answer("p", q.ID, "yes", OperatorScope{}); err != nil || !reservedPersonTurn(cmd.TurnID) {
		t.Fatalf("parked answer: %+v, %v", cmd, err)
	}
	// a master's answer and a default keep their own, unreserved, ids
	q2, _ := st.Park("p", "a", "risky_action", "again?", nil, "", false)
	if _, cmd, err = st.Answer("p", q2.ID, "no", masterScope{conv: "m"}); err == nil && reservedPersonTurn(cmd.TurnID) {
		t.Fatalf("a master's answer carries a person id: %+v", cmd)
	}
}
