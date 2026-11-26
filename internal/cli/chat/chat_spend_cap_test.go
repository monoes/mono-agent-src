package chat

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitask/budget"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

func spendCapOf(t *testing.T, dbPath, conv string) chatSpendCap {
	t.Helper()
	out, code := runChatHistory(t, dbPath, "default", "spend-cap", conv)
	if code != 0 {
		t.Fatalf("spend-cap: exit %d %s", code, out)
	}
	var got chatSpendCap
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.ConversationID != conv || got.V != 1 {
		t.Fatalf("spend-cap output %s: %v", out, err)
	}
	return got
}

// spendCapRig is a database with one conversation at access on runtime and a
// global AI-task cap of $100.
func spendCapRig(t *testing.T, access, runtime string, settings coderSettings) (dbPath, conv string) {
	t.Helper()
	dbPath = newChatCLITestDB(t)
	settings.Enabled = true
	setCoderSettings(t, dbPath, settings)
	c, err := openTestChatStore(t, dbPath).CreateConversationAccess("default", "agent", "general", runtime, "", "", access, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	withSpendDB(t, dbPath, func(db *storage.Database) {
		if err := (&budget.Ledger{DB: db.DB}).SetCap(budget.ScopeGlobal, "", 100, "test"); err != nil {
			t.Fatal(err)
		}
	})
	return dbPath, c.ID
}

func withSpendDB(t *testing.T, dbPath string, fn func(db *storage.Database)) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

func setSessionBudget(t *testing.T, dbPath, conv string, usd float64) {
	t.Helper()
	withSpendDB(t, dbPath, func(db *storage.Database) {
		if err := coderhost.NewStore(db.DB).SetBudget("default", conv, usd); err != nil {
			t.Fatal(err)
		}
	})
}

// #1766: the pane's Spend row shows what bounds this chat's messages, not the
// AI-task budget of other work.
func TestSpendCapOfAPlainCoderChat(t *testing.T) {
	db, conv := spendCapRig(t, ai.AccessFull, "claude", coderSettings{BudgetUSD: 0.5})
	if got := spendCapOf(t, db, conv); !got.Bounded || got.CapUSD != 0.5 || got.Source != "message" || got.UsedUp || got.WorkersCapUSD != 0 {
		t.Errorf("coder cap: %+v", got)
	}
	db, conv = spendCapRig(t, ai.AccessFull, "claude", coderSettings{})
	if got := spendCapOf(t, db, conv); got.Bounded || got.CapUSD != 0 || got.Source != "" {
		t.Errorf("no coder cap, but a global AI-task cap, which bounds no plain chat: %+v", got)
	}
	db, conv = spendCapRig(t, ai.AccessScoped, "claude", coderSettings{BudgetUSD: 0.5})
	if got := spendCapOf(t, db, conv); got.Bounded {
		t.Errorf("a scoped chat does not run under the coder cap: %+v", got)
	}
	db, conv = spendCapRig(t, ai.AccessFull, "kilo", coderSettings{BudgetUSD: 0.5})
	if got := spendCapOf(t, db, conv); got.UnenforceableOn != "kilo" || got.CapUSD != 0.5 {
		t.Errorf("kilo: %+v", got)
	}
}

// Kilo drops the per-message cap but refuses a turn a session budget caps, so
// the session's limit is the one named even when the dropped cap is lower.
func TestSpendCapOfAKiloSessionNamesTheCapThatRefuses(t *testing.T) {
	db, conv := spendCapRig(t, ai.AccessFull, "kilo", coderSettings{BudgetUSD: 0.5})
	setSessionBudget(t, db, conv, 3)
	if got := spendCapOf(t, db, conv); got.Source != "session" || got.UnenforceableOn != "kilo" {
		t.Errorf("kilo session: %+v", got)
	}
}

func TestSpendCapOfADynamicOrgChatNamesTheWorkersCap(t *testing.T) {
	db, conv := spendCapRig(t, ai.AccessFull, "claude", coderSettings{BudgetUSD: 0.5, OrgBudgetUSD: 2})
	if err := openTestChatStore(t, db).SetConversationOrgMode(conv, "default", ai.OrgModeDynamic); err != nil {
		t.Fatal(err)
	}
	if got := spendCapOf(t, db, conv); got.CapUSD != 0.5 || got.WorkersCapUSD != 2 {
		t.Errorf("dynamic org: %+v", got)
	}
}

func TestSpendCapOfASessionWithABudget(t *testing.T) {
	db, conv := spendCapRig(t, ai.AccessFull, "claude", coderSettings{BudgetUSD: 0.5})
	setSessionBudget(t, db, conv, 0.3)
	if got := spendCapOf(t, db, conv); got.Source != "session" || got.CapUSD != 0.3 || got.TotalUSD != 0.3 || got.UsedUp {
		t.Errorf("session below the coder cap: %+v", got)
	}
	setSessionBudget(t, db, conv, 3)
	if got := spendCapOf(t, db, conv); got.Source != "message" || got.CapUSD != 0.5 || got.TotalUSD != 0 {
		t.Errorf("coder cap below what the session has left: %+v", got)
	}
	setSessionBudget(t, db, conv, 1e-9)
	if got := spendCapOf(t, db, conv); got.Source != "session" || !got.UsedUp || got.CapUSD != 0 || !got.Bounded {
		t.Errorf("used up: %+v", got)
	}
}

// A task's chat: the figure is the grant its next turn really gets.
func TestSpendCapOfATaskChatMatchesTheTurnsGrant(t *testing.T) {
	r := newTaskRig(t) // global cap $100, task cap $5
	task, _ := r.task(t, "default", aitask.StatusNeedsUser)
	withSpendDB(t, r.dbPath, func(db *storage.Database) {
		l := &budget.Ledger{DB: db.DB}
		if err := l.SetCap(budget.ScopeConversation, r.convID, 2, "test"); err != nil {
			t.Fatal(err)
		}
	})
	got := spendCapOf(t, r.dbPath, r.convID)
	if got.Source != "task" || got.CapUSD != 2 || got.TotalUSD != 2 || got.UsedUp || got.TaskID != task.ID {
		t.Errorf("task: %+v", got)
	}
	run := taskTurn(testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"}), r.convID, nil)
	if run.err != nil || run.budget == nil || *run.budget != got.CapUSD {
		t.Errorf("the turn was granted %v (%v), the pane says %v", run.budget, run.err, got.CapUSD)
	}
}

func TestSpendCapOfATaskChatWithNoCapIsRefused(t *testing.T) {
	r := newTaskRig(t)
	r.task(t, "default", aitask.StatusNeedsUser)
	withSpendDB(t, r.dbPath, func(db *storage.Database) {
		if err := (&budget.Ledger{DB: db.DB}).ClearCap(budget.ScopeGlobal, ""); err != nil {
			t.Fatal(err)
		}
	})
	if got := spendCapOf(t, r.dbPath, r.convID); got.Source != "task" || !got.UsedUp || !got.Bounded {
		t.Errorf("no cap applies to the task, so its turn does not run: %+v", got)
	}
}

// A generic task that is over leaves an ordinary coder chat.
func TestSpendCapAfterAGenericTaskIsOver(t *testing.T) {
	r := newTaskRig(t)
	r.task(t, "default", aitask.StatusComplete)
	if got := spendCapOf(t, r.dbPath, r.convID); got.Bounded {
		t.Errorf("a finished generic task no longer bounds the chat: %+v", got)
	}
}

func TestSpendCapOfAnUnknownConversation(t *testing.T) {
	db := newChatCLITestDB(t)
	if out, code := runChatHistory(t, db, "default", "spend-cap", "nope"); code != 2 {
		t.Errorf("exit %d %s, want not found (2)", code, out)
	}
}
