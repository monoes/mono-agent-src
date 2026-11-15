package chat

import (
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

func TestSessionsDecisionsAndQuestionsCLI(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cs := coderhost.NewStore(db.DB)
	e, err := cs.Record(coderhost.LedgerEntry{ProfileID: "default", ConversationID: conv.ID, Class: "ambiguity", Question: "q", Choice: "c", Reversible: true, MadeBy: "model"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := cs.Park("default", conv.ID, "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	if out, code := runSessionsCLI(t, cfg, "decisions", "list", conv.ID); code != 0 || !strings.Contains(out, `"choice": "c"`) && !strings.Contains(out, `"choice":"c"`) {
		t.Fatalf("list: %d %s", code, out)
	}
	if out, code := runSessionsCLI(t, cfg, "questions", "list"); code != 0 || !strings.Contains(out, "force push?") || !strings.Contains(out, `"user"`) {
		t.Fatalf("questions list: %d %s", code, out)
	}

	// the mutating verbs are the operator's: without an operator check they fail closed (exit 3) and change nothing
	agent := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	agent.RequireOperator = nil
	if _, code := runSessionsCLI(t, agent, "decisions", "revert", conv.ID, "1"); code != 3 {
		t.Fatalf("revert by a non-operator: exit %d", code)
	}
	if _, code := runSessionsCLI(t, agent, "questions", "answer", conv.ID, "1", "yes", "--no-host"); code != 3 {
		t.Fatalf("answer by a non-operator: exit %d", code)
	}
	if got, _, _ := openCoder(t, dbPath).GetDecision("default", conv.ID, e.ID); got.Status != "active" {
		t.Fatal("a refused revert changed the ledger")
	}
	if openCoder(t, dbPath).OpenParked("default", conv.ID) != 1 {
		t.Fatal("a refused answer closed the question")
	}

	if _, code := runSessionsCLI(t, cfg, "decisions", "revert", conv.ID, "1"); code != 0 {
		t.Fatalf("operator revert: exit %d", code)
	}
	if _, code := runSessionsCLI(t, cfg, "decisions", "revert", conv.ID, "1"); code == 0 {
		t.Fatal("a second revert succeeded")
	}
	if _, code := runSessionsCLI(t, cfg, "questions", "answer", conv.ID, "1", "no", "--no-host"); code != 0 {
		t.Fatalf("operator answer: exit %d", code)
	}
	cmds := pendingCommands(t, dbPath)
	if len(cmds) != 1 || !strings.HasPrefix(cmds[0].TurnID, coderhost.AnswerPrefix) || !strings.Contains(cmds[0].Message, "no") {
		t.Fatalf("queued = %+v", cmds)
	}
	_ = q
}

// An answer that was queued but whose session could not be released is accepted: the
// result carries the queued command and a warning, and the exit is 0 (repeating it would fail).
func TestSessionsAnswerQueuedButSessionNotReleasedIsAcceptedWithAWarning(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	cs := openCoder(t, dbPath)
	q, err := cs.Park("default", conv.ID, "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER no_release BEFORE UPDATE ON coder_sessions WHEN NEW.state = 'idle' BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	out, code := runSessionsCLI(t, cfg, "questions", "answer", conv.ID, strconv.FormatInt(q.ID, 10), "yes", "--no-host")
	if code != 0 || !strings.Contains(out, "commandId") || !strings.Contains(out, "warning") || !strings.Contains(out, `"released": false`) && !strings.Contains(out, `"released":false`) || !strings.Contains(out, "do not answer again") {
		t.Fatalf("exit %d, output %q", code, out)
	}
	if cmds := pendingCommands(t, dbPath); len(cmds) != 1 {
		t.Fatalf("queued = %+v", cmds)
	}
	// repeating it is refused: the question is closed, and the session still waits
	if _, code := runSessionsCLI(t, cfg, "questions", "answer", conv.ID, strconv.FormatInt(q.ID, 10), "yes", "--no-host"); code != 3 {
		t.Fatalf("repeat answer exit %d, want 3", code)
	}
	cs = openCoder(t, dbPath)
	if got, err := cs.GetQuestion("default", q.ID); err != nil || got.Status == "open" {
		t.Fatalf("question = %+v, %v", got, err)
	}
	if x, _, _ := cs.GetSession("default", conv.ID); x.State != coderhost.StateWaitingUser {
		t.Fatalf("session state = %s", x.State)
	}
}

func openCoder(t *testing.T, dbPath string) *coderhost.Store {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return coderhost.NewStore(db.DB)
}
