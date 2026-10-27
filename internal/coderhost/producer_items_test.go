package coderhost

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func openItems(t *testing.T, st *Store) []PersonItem {
	t.Helper()
	items, err := st.OpenPersonItems("p")
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func setState(t *testing.T, st *Store, conv string, state State) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE coder_sessions SET state = ? WHERE profile_id = 'p' AND conversation_id = ?`, string(state), conv); err != nil {
		t.Fatal(err)
	}
}

// A child of a stopped master has no master to ask: the person answers, and is told.
func TestChildOfAStoppedMasterIsMasterless(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	setState(t, st, "m", StateStopped)
	if _, ok := st.hasLiveMaster("p", "k1"); ok {
		t.Fatal("a stopped master counts as live")
	}
	if _, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "ambiguity", Question: "q"}); !errors.Is(err, ErrNoMasterDecide) {
		t.Fatalf("non-risky park = %v", err)
	}
	q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "risky_action", Question: "force push?"})
	if err != nil || q.Answerer != WhoUser {
		t.Fatalf("risky park = %+v %v", q, err)
	}
	if items := openItems(t, st); len(items) != 1 || items[0].RefID != q.ID {
		t.Fatalf("items = %+v", items)
	}
	if _, _, err := st.Answer("p", q.ID, "no", OperatorScope{}); err != nil {
		t.Fatalf("the operator could not answer: %v", err)
	}
	if len(openItems(t, st)) != 0 {
		t.Fatal("answered item still open")
	}
}

func TestChildOfADeletedMasterIsMasterless(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	if _, err := st.db.Exec(`DELETE FROM coder_sessions WHERE profile_id = 'p' AND conversation_id = 'm'`); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.hasLiveMaster("p", "k1"); ok {
		t.Fatal("a deleted master counts as live")
	}
	if _, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "scope_growth", Question: "q"}); !errors.Is(err, ErrNoMasterDecide) {
		t.Fatalf("non-risky park = %v", err)
	}
	if q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "missing_credential", Question: "token?"}); err != nil || q.Answerer != WhoUser || len(openItems(t, st)) != 1 {
		t.Fatalf("risky park = %+v %v", q, err)
	}
}

// The answerer follows the master as it is now, in both directions, and the person is told exactly
// while they are the one who has to answer.
func TestPersonItemFollowsTheMasterAfterThePark(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "risky_action", Question: "go?"})
	if err != nil || q.Answerer != WhoMaster {
		t.Fatalf("park under a master = %+v %v", q, err)
	}
	if itemCount(t, st) != 1 || len(openItems(t, st)) != 0 {
		t.Fatalf("items %d open %d: a question the master answers is not the person's", itemCount(t, st), len(openItems(t, st)))
	}
	// the master is stopped: the person has to answer
	setState(t, st, "m", StateStopped)
	if len(openItems(t, st)) != 1 {
		t.Fatal("not counted after the master stopped")
	}
	setState(t, st, "m", StateIdle)
	if len(openItems(t, st)) != 0 {
		t.Fatal("still counted after the master came back")
	}
	// the child is made solo: the person has to answer, with no row written at that time
	if err := st.SetRole("p", "k1", "solo", ""); err != nil {
		t.Fatal(err)
	}
	if items := openItems(t, st); len(items) != 1 || items[0].RefID != q.ID {
		t.Fatalf("not counted after the child became solo: %+v", items)
	}
	// and linked to a master again: the master's to answer, the person is not told
	if err := st.SetRole("p", "k1", "child", "m"); err != nil {
		t.Fatal(err)
	}
	if len(openItems(t, st)) != 0 {
		t.Fatal("counted although the master answers")
	}
}

func TestMasterlessItemThenChildIsNotCountedAndItsAnswererIsTheMaster(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "m", "/w/m")
	addConversation(t, st, "k1", "/w/k1")
	if err := st.SetRole("p", "m", "master", ""); err != nil {
		t.Fatal(err)
	}
	q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "risky_action", Question: "go?"})
	if err != nil || len(openItems(t, st)) != 1 {
		t.Fatalf("park = %+v %v", q, err)
	}
	if err := st.SetRole("p", "k1", "child", "m"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetQuestion("p", q.ID)
	if got.Answerer != WhoMaster || len(openItems(t, st)) != 0 {
		t.Fatalf("answerer %s, open %d", got.Answerer, len(openItems(t, st)))
	}
}

// A person item is never default-closed, whatever a (hand-stored) policy says for its class or
// for a question that was made an item.
func TestAPersonItemTakesNoDefault(t *testing.T) {
	st, clk := parkStore(t)
	childOf(t, st)
	setSettings(t, st, "k1", `{"autonomy_classes": {"ambiguity": {"who": "master", "fallback": "take_default"}}}`)
	q, err := st.Park("p", "k1", "ambiguity", "which?", nil, "a", false) // a master's to answer; a default would be taken
	if err != nil {
		t.Fatal(err)
	}
	// the same question, made an item by hand (addPersonItem itself refuses a non-hard class)
	if err := st.addPersonItem("p", "k1", ItemParkedQuestion, "ambiguity", q.ID); err == nil {
		t.Fatal("addPersonItem took a non-hard class")
	}
	if _, err := st.db.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p', 'k1', 'parked_question', ?, 'x')`, q.ID); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(10 * ParkTimeout)
	if n, err := st.ApplyDefaults(); err != nil || n != 0 {
		t.Fatalf("applied %d (%v)", n, err)
	}
	if got, _ := st.GetQuestion("p", q.ID); got.Status != "open" {
		t.Fatalf("status = %s", got.Status)
	}
	// the control: the same question without the item does take its default
	q2, _ := st.Park("p", "k1", "ambiguity", "which again?", nil, "b", false)
	clk.t = clk.t.Add(10 * ParkTimeout)
	if n, _ := st.ApplyDefaults(); n != 1 {
		t.Fatalf("the control applied %d", n)
	}
	if got, _ := st.GetQuestion("p", q2.ID); got.Status != "defaulted" {
		t.Fatalf("control status = %s", got.Status)
	}
}

func TestMasterlessHardParksAreRateLimited(t *testing.T) {
	st, clk := soloStore(t)
	for i := 0; i < MaxPersonItemsPerHour; i++ {
		if _, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"}); err != nil {
			t.Fatalf("park %d: %v", i, err)
		}
	}
	before := st.OpenParked("p", "solo")
	if _, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "missing_credential", Question: "q"}); !errors.Is(err, ErrPersonItemRate) || !errors.Is(err, ErrLimit) {
		t.Fatalf("over the rate = %v", err)
	}
	if st.OpenParked("p", "solo") != before || len(openItems(t, st)) != MaxPersonItemsPerHour {
		t.Fatal("a refused park left a question or item")
	}
	clk.t = clk.t.Add(61 * time.Minute)
	if _, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"}); err != nil {
		t.Fatalf("after the window = %v", err)
	}
	// under a live master the limit does not apply (the master answers)
	st2, _ := parkStore(t)
	childOf(t, st2)
	for i := 0; i < MaxPersonItemsPerHour+2; i++ {
		if _, err := st2.ParkOwn(caller("k1"), ParkRequest{Class: "risky_action", Question: "q"}); err != nil {
			t.Fatalf("mastered park %d: %v", i, err)
		}
	}
}

// A stopped session takes no answer (ErrStopped), so it is not counted as waiting for one.
func TestStoppedSessionsItemsAreNotCounted(t *testing.T) {
	st, _ := soloStore(t)
	q, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"})
	if err != nil || len(openItems(t, st)) != 1 {
		t.Fatalf("park = %+v %v", q, err)
	}
	setState(t, st, "solo", StateStopped)
	if _, _, err := st.Answer("p", q.ID, "x", OperatorScope{}); !errors.Is(err, ErrStopped) {
		t.Fatalf("answer = %v", err)
	}
	if len(openItems(t, st)) != 0 {
		t.Fatal("a stopped session's item is counted")
	}
	setState(t, st, "solo", StateIdle)
	if len(openItems(t, st)) != 1 {
		t.Fatal("not counted again once resumed")
	}
}

// The item rows go with their question: pruned closed questions and dropped ones leave none behind.
func TestPersonItemsGoWithTheirQuestion(t *testing.T) {
	st, _ := soloStore(t)
	q, _ := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"})
	st.dropQuestion(q.ID)
	if itemCount(t, st) != 0 {
		t.Fatal("dropQuestion left its item")
	}
	// a question that is gone by another way (pruned) leaves an item that pruning removes
	if _, err := st.db.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p', 'solo', 'parked_question', 999, 'x')`); err != nil {
		t.Fatal(err)
	}
	st.pruneClosedQuestions("p", "solo")
	if itemCount(t, st) != 0 {
		t.Fatal("pruning left an item whose question is gone")
	}
	// one that still has its question stays
	q2, _ := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q2"})
	st.pruneClosedQuestions("p", "solo")
	if itemCount(t, st) != 1 || len(openItems(t, st)) != 1 || openItems(t, st)[0].RefID != q2.ID {
		t.Fatal("pruning removed a live item")
	}
	if !strings.Contains(ErrPersonItemRate.Error(), "risky questions") {
		t.Fatal(ErrPersonItemRate)
	}
}

// The master cannot take a risky question when it is paused, at its check-in limit or its notes keep
// failing: the person owns it then (counted, and the operator may answer). With a master that can
// take it, the master answers and the operator is refused as before.
func TestRiskyQuestionBelongsToThePersonWhenTheMasterCannotTakeIt(t *testing.T) {
	setup := func(t *testing.T) (*Store, Question) {
		st, _ := parkStore(t)
		childOf(t, st)
		q, err := st.ParkOwn(caller("k1"), ParkRequest{Class: "risky_action", Question: "go?"})
		if err != nil || q.Answerer != WhoMaster {
			t.Fatalf("park = %+v %v", q, err)
		}
		if len(openItems(t, st)) != 0 {
			t.Fatal("counted while the master can answer")
		}
		if _, _, err := st.Answer("p", q.ID, "x", OperatorScope{}); !errors.Is(err, ErrNotAnswerer) {
			t.Fatalf("the operator answered a question the master takes: %v", err)
		}
		return st, q
	}
	answered := func(t *testing.T, st *Store, q Question) {
		t.Helper()
		if len(openItems(t, st)) != 1 {
			t.Fatal("not in needs-you")
		}
		_, cmd, err := st.Answer("p", q.ID, "no", OperatorScope{})
		if err != nil || !strings.HasPrefix(cmd.TurnID, AnswerPrefix+answerFromPerson+"-") || cmd.Issuer != "" || cmd.WantsMission {
			t.Fatalf("operator answer = %+v %v", cmd, err)
		}
		if len(openItems(t, st)) != 0 {
			t.Fatal("still counted")
		}
	}
	t.Run("paused master", func(t *testing.T) {
		st, q := setup(t)
		setState(t, st, "m", StatePaused)
		answered(t, st, q)
	})
	t.Run("master at its check-in limit", func(t *testing.T) {
		st, q := setup(t)
		setSettings(t, st, "k1", `{"checkin_limit": 1}`)
		addTurn(t, st, "k1", "checkin-aaaa", "completed", "digest", t0)
		if _, _, err := st.AnswerAsMaster(caller("m"), "k1", q.ID, "yes"); !errors.Is(err, ErrWaitsForPerson) {
			t.Fatalf("the master at its limit = %v", err)
		}
		answered(t, st, q)
	})
	t.Run("notes keep failing", func(t *testing.T) {
		st, q := setup(t)
		if _, err := st.db.Exec(`UPDATE coder_session_questions SET reopen_count = ? WHERE id = ?`, MaxAnswerReopens, q.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.AnswerAsMaster(caller("m"), "k1", q.ID, "yes"); !errors.Is(err, ErrAnswerKeepsFailing) {
			t.Fatalf("the master with failing notes = %v", err)
		}
		answered(t, st, q)
	})
	t.Run("a failure in the last half hour", func(t *testing.T) {
		st, q := setup(t)
		if _, err := st.db.Exec(`UPDATE coder_session_questions SET last_failed_at = ? WHERE id = ?`, st.now().UTC().Format(timeLayout), q.ID); err != nil {
			t.Fatal(err)
		}
		answered(t, st, q)
	})
}

// A hard question and its item are one write: when the item cannot be written the question is not
// left behind, and concurrent parks of a session with no master cannot pass the hourly limit.
func TestHardParkIsOneWriteAndTheLimitIsAtomic(t *testing.T) {
	st, _ := soloStore(t)
	if _, err := st.db.Exec(`CREATE TRIGGER fail_item BEFORE INSERT ON coder_session_person_items BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"}); err == nil {
		t.Fatal("parked although the item could not be written")
	}
	if st.OpenParked("p", "solo") != 0 {
		t.Fatal("a hard question was left without its item")
	}
	if _, err := st.db.Exec(`DROP TRIGGER fail_item`); err != nil {
		t.Fatal(err)
	}

	stores := []*Store{st, NewStore(st.db), NewStore(st.db), NewStore(st.db)}
	for _, s := range stores {
		s.now = st.now
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			if _, err := s.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(stores[i%len(stores)])
	}
	wg.Wait()
	if ok != MaxPersonItemsPerHour || itemCount(t, st) != MaxPersonItemsPerHour || st.OpenParked("p", "solo") != MaxPersonItemsPerHour {
		t.Fatalf("%d parks passed, %d items, %d open (limit %d)", ok, itemCount(t, st), st.OpenParked("p", "solo"), MaxPersonItemsPerHour)
	}
}

// The person's list is oldest first.
func TestOpenPersonItemsAreOldestFirst(t *testing.T) {
	st, clk := soloStore(t)
	var ids []int64
	for i := 0; i < 3; i++ {
		q, err := st.ParkOwn(caller("solo"), ParkRequest{Class: "risky_action", Question: "q"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, q.ID)
		clk.t = clk.t.Add(time.Second)
	}
	items := openItems(t, st)
	if len(items) != 3 || items[0].RefID != ids[0] || items[2].RefID != ids[2] {
		t.Fatalf("items = %+v", items)
	}
}
