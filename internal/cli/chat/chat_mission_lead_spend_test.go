package chat

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// leadSpendMission is a dynamic-org conversation with a running mission whose lead is a scripted claude: every
// exec takes the next line of the costs file, "<cost_usd> [warn]", and reports it for the one claude session
// (a running total for the session, as claude reports it). "warn" asks for a compaction.
// turn runs one chat turn and returns what the mission's meter and the conversation's spend say after it.
func leadSpendMission(t *testing.T, costs string) (turn func(id string) (meter, conversation float64)) {
	t.Helper()
	return newLeadSpendRig(t, costs, true).turn
}

// leadSpendRig is that conversation, its mission's session, and a gate: an exec whose costs line says "hold" as
// its third word waits before its result until release (atGate waits for it to get there). In place of a cost a line
// may say "none" (the result reports none), "auth" (the runtime starts and is refused) or "nostart" (it dies first).
// send runs one turn and returns its error; argsLog holds the runtime's argument lines and systemFile the system
// prompt of its latest exec.
type leadSpendRig struct {
	send       func(id string) error
	spendCap   func(t *testing.T) chatSpendCap
	argsLog    string
	systemFile string

	turn    func(id string) (meter, conversation float64)
	meter   func() (float64, tasks.SessionState)
	atGate  func()
	release func()
	store   *tasks.Store
	session int64
}

// newLeadSpendRig builds the rig; without approve the mission stays a draft.
func newLeadSpendRig(t *testing.T, costs string, approve bool) leadSpendRig {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	queue := filepath.Join(t.TempDir(), "costs")
	if err := os.WriteFile(queue, []byte(costs), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(t.TempDir(), "gate")
	systemFile := filepath.Join(t.TempDir(), "system")
	bin, argsLog := writeCoderMonomind(t, `  prev=""
  for a in "$@"; do [ "$prev" = "--system-file" ] && cp "$a" '`+systemFile+`'; prev="$a"; done
  set -- $(head -n 1 '`+queue+`')
  tail -n +2 '`+queue+`' > '`+queue+`.next'; mv '`+queue+`.next' '`+queue+`'
  warn=""
  if [ "$2" = warn ]; then warn=',"peak_context_tokens":250000,"context_warning":true'; fi
  cost=',"cost_usd":'"$1"
  [ "$1" = none ] && cost=""
  [ "$1" = nostart ] && exit 3
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  if [ "$1" = auth ]; then
    echo '{"v":1,"type":"error","code":"auth","message":"not signed in","fatal":true}'
    echo '{"v":1,"type":"done","exit_code":1}'
    exit 1
  fi
  echo '{"v":1,"type":"session","session_id":"th_lead"}'
  if [ "$3" = hold ]; then
    : > '`+gate+`.reached'
    i=0
    while [ ! -f '`+gate+`.release' ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done
    rm -f '`+gate+`.release'
  fi
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"'"$cost$warn"'}'
  echo '{"v":1,"type":"done","exit_code":0}'`)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatalf("set-org: exit %d", code)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, human, ctx := tasks.NewStore(db.DB), tasks.Actor{Kind: tasks.Human}, context.Background()
	se, err := store.CreateSession(ctx, "default", tasks.SessionInput{Title: "M", Goal: "g", BudgetUSD: 5, ConversationID: conv.ID, Kind: tasks.SessionKindMission}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "T", Notes: "do it", Acceptance: "done", SuggestedExecutor: "lead"}, human); err != nil {
		t.Fatal(err)
	}
	if approve {
		if _, err := store.ApproveSession(ctx, "default", se.ID, human); err != nil {
			t.Fatal(err)
		}
	}
	rig := leadSpendRig{store: store, session: se.ID, argsLog: argsLog, systemFile: systemFile}
	rig.spendCap = func(t *testing.T) chatSpendCap {
		t.Helper()
		return spendCapOf(t, dbPath, conv.ID)
	}
	rig.send = func(id string) error {
		t.Helper()
		_, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", id, "--", "work")
		return err
	}
	rig.meter = func() (float64, tasks.SessionState) {
		t.Helper()
		got, err := store.GetSession(ctx, "default", se.ID, human)
		if err != nil {
			t.Fatal(err)
		}
		return got.SpentUSD, got.State
	}
	rig.turn = func(id string) (float64, float64) {
		t.Helper()
		if err := rig.send(id); err != nil {
			t.Fatalf("turn %s: %v", id, err)
		}
		spent, err := coderspend.Spent(db.DB, "default", conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		meter, _ := rig.meter()
		return meter, spent
	}
	rig.atGate = func() {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); os.Remove(gate+".reached") != nil; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the lead's exec never reached its gate")
			}
		}
	}
	rig.release = func() {
		t.Helper()
		if err := os.WriteFile(gate+".release", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func wantSpend(t *testing.T, after string, meter, conversation, want float64) {
	t.Helper()
	if math.Abs(meter-want) > 1e-9 || math.Abs(conversation-want) > 1e-9 {
		t.Errorf("after %s: the mission's meter says $%v and the conversation's spend $%v, want $%v for both", after, meter, conversation, want)
	}
}

// A resumed claude session reports its total so far. The mission's meter is charged what each lead turn added,
// the same figure the conversation's spend counts, never the earlier turns again (#1751).
func TestMissionLeadTurnsAreChargedOnce(t *testing.T) {
	turn := leadSpendMission(t, "0.41\n0.85\n0.9\n")
	meter, conv := turn("t1")
	wantSpend(t, "turn 1", meter, conv, 0.41)
	meter, conv = turn("t2")
	wantSpend(t, "turn 2", meter, conv, 0.85)
	meter, conv = turn("t3")
	wantSpend(t, "turn 3", meter, conv, 0.9)
}

// The compaction at a turn's end resumes the same session, so its figure includes the turn's: it is counted once,
// and charged to the mission with the turn it belongs to.
func TestMissionLeadCompactionIsChargedOnce(t *testing.T) {
	turn := leadSpendMission(t, "0.41 warn\n0.5\n0.6\n")
	meter, conv := turn("t1")
	wantSpend(t, "the compacted turn", meter, conv, 0.5)
	meter, conv = turn("t2")
	wantSpend(t, "the turn after it", meter, conv, 0.6)
}
