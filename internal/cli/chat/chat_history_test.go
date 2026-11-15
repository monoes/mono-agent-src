package chat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/storage"
)

// openTestChatStore opens the test DB's chat store for seeding/asserting.
func openTestChatStore(t *testing.T, dbPath string) *ai.AIStore {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := ai.NewAIStore(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// runChatHistory runs `chat history <args>` with --json and returns stdout
// and the exit code the error maps to.
func runChatHistory(t *testing.T, dbPath, profile string, args ...string) (string, int) {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: profile, JSONOutput: true})
	var err error
	out := captureStdout(t, func() {
		cmd := newChatCmd(cfg)
		cmd.SetArgs(append([]string{"history"}, args...))
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return out, exitCodeFor(err)
}

func decodeChatJSON(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
}

func TestChatHistoryCreateListShowAndPaging(t *testing.T) {
	dbPath := newChatCLITestDB(t)

	out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--model", "sonnet")
	if code != 0 {
		t.Fatalf("create exit %d: %s", code, out)
	}
	var created map[string]any
	decodeChatJSON(t, out, &created)
	for _, k := range []string{"id", "profile_id", "backend", "workflow_context", "runtime_id", "model", "session_id", "created_at", "updated_at"} {
		if _, ok := created[k]; !ok {
			t.Errorf("create output lacks %q: %s", k, out)
		}
	}
	if created["backend"] != "agent" || created["workflow_context"] != "general" || created["runtime_id"] != "claude" || created["model"] != "sonnet" {
		t.Errorf("created = %s", out)
	}
	if strings.Contains(out, "history_key") {
		t.Errorf("the history key must never be printed: %s", out)
	}
	for i := 0; i < 2; i++ {
		if _, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "codex", "--workflow", "draft"); code != 0 {
			t.Fatalf("create #%d exit %d", i, code)
		}
	}

	var page chatConversationPage
	out, _ = runChatHistory(t, dbPath, "default", "list", "--limit", "2")
	decodeChatJSON(t, out, &page)
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first page = %s", out)
	}
	out, _ = runChatHistory(t, dbPath, "default", "list", "--limit", "2", "--cursor", page.NextCursor)
	var page2 chatConversationPage
	decodeChatJSON(t, out, &page2)
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("second page = %s", out)
	}
	seen := map[string]bool{}
	for _, c := range append(page.Items, page2.Items...) {
		seen[c.ID] = true
	}
	if len(seen) != 3 || !seen[created["id"].(string)] {
		t.Errorf("paging did not return each conversation once: %v", seen)
	}

	out, code = runChatHistory(t, dbPath, "default", "show", created["id"].(string))
	if code != 0 || !strings.Contains(out, `"runtime_id": "claude"`) {
		t.Errorf("show exit %d: %s", code, out)
	}
	if _, code = runChatHistory(t, dbPath, "default", "show", "nope"); code != 2 {
		t.Errorf("show unknown exit %d, want 2", code)
	}
	if _, code = runChatHistory(t, dbPath, "default", "create"); code != 3 {
		t.Errorf("create without --runtime exit %d, want 3", code)
	}
	if _, code = runChatHistory(t, dbPath, "default", "list", "--limit", "500"); code != 3 {
		t.Errorf("list --limit 500 exit %d, want 3", code)
	}
}

func TestChatHistoryEmptyListsAreArrays(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	out, _ := runChatHistory(t, dbPath, "default", "list")
	if !strings.Contains(out, `"items": []`) {
		t.Errorf("empty list = %s, want items []", out)
	}
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	out, _ = runChatHistory(t, dbPath, "default", "turns", conv.ID)
	if !strings.Contains(out, `"items": []`) {
		t.Errorf("empty turns = %s, want items []", out)
	}
	store.CreateTurn(conv.ID, "default", "t1", "", "hi")
	out, _ = runChatHistory(t, dbPath, "default", "events", conv.ID, "t1")
	if !strings.Contains(out, `"items": []`) {
		t.Errorf("empty events = %s, want items []", out)
	}
}

func TestChatHistoryTurnsAndEventsPaging(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversation("default", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if _, _, err := store.CreateTurn(conv.ID, "default", id, "inst-a", "prompt "+id); err != nil {
			t.Fatal(err)
		}
		if id != "t3" {
			store.FinalizeTurn("default", conv.ID, id, chatevents.StatusCompleted, "end_turn", nil, true)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := store.AppendEvent("default", conv.ID, "t3", chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "part-1", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	var turns chatTurnPage
	out, code := runChatHistory(t, dbPath, "default", "turns", conv.ID, "--limit", "2")
	if code != 0 {
		t.Fatalf("turns exit %d: %s", code, out)
	}
	decodeChatJSON(t, out, &turns)
	if len(turns.Items) != 2 || turns.NextCursor == "" {
		t.Fatalf("turns page 1 = %s", out)
	}
	if turns.Items[0].OwnerInstanceID != "inst-a" {
		t.Errorf("turns must carry owner_instance_id: %s", out)
	}
	out, _ = runChatHistory(t, dbPath, "default", "turns", conv.ID, "--limit", "2", "--cursor", turns.NextCursor)
	var turns2 chatTurnPage
	decodeChatJSON(t, out, &turns2)
	if len(turns2.Items) != 1 || turns2.NextCursor != "" {
		t.Fatalf("turns page 2 = %s", out)
	}
	if _, code := runChatHistory(t, dbPath, "default", "turns", "nope"); code != 2 {
		t.Errorf("turns of an unknown conversation exit %d, want 2", code)
	}

	var evs chatEventPage
	out, code = runChatHistory(t, dbPath, "default", "events", conv.ID, "t3", "--limit", "2")
	if code != 0 {
		t.Fatalf("events exit %d: %s", code, out)
	}
	decodeChatJSON(t, out, &evs)
	if len(evs.Items) != 2 || evs.Items[0].Seq != 1 || !evs.HasMore || evs.LastCommittedSeq != 5 || evs.Turn.Status != "active" {
		t.Fatalf("events page 1 = %s", out)
	}
	for _, k := range []string{`"conversation_id"`, `"turn_id"`, `"profile_id"`, `"partId"`} {
		if !strings.Contains(out, k) {
			t.Errorf("events output lacks %s (snake_case envelope, verbatim payload): %s", k, out)
		}
	}
	out, _ = runChatHistory(t, dbPath, "default", "events", conv.ID, "t3", "--after-seq", "3", "--limit", "10")
	var evs2 chatEventPage
	decodeChatJSON(t, out, &evs2)
	if len(evs2.Items) != 2 || evs2.Items[0].Seq != 4 || evs2.HasMore {
		t.Fatalf("events after 3 = %s", out)
	}
	if _, code := runChatHistory(t, dbPath, "default", "events", conv.ID, "missing"); code != 2 {
		t.Errorf("events of an unknown turn exit %d, want 2", code)
	}
	other, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	if _, code := runChatHistory(t, dbPath, "default", "events", other.ID, "t3"); code != 2 {
		t.Errorf("events of a turn under the wrong conversation exit %d, want 2", code)
	}

	out, code = runChatHistory(t, dbPath, "default", "turn", conv.ID, "t1")
	if code != 0 || !strings.Contains(out, `"status": "completed"`) {
		t.Errorf("turn exit %d: %s", code, out)
	}
}

func TestChatHistoryDeleteRefusesActiveTurn(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	store.CreateTurn(conv.ID, "default", "t1", "inst", "hi")
	store.AppendEvent("default", conv.ID, "t1", chatevents.EventTurnStarted, chatevents.TurnStartedPayload{Backend: "agent", Text: "hi"})

	if _, code := runChatHistory(t, dbPath, "default", "delete", conv.ID); code != 3 {
		t.Fatalf("delete with an active turn exit %d, want 3", code)
	}
	if _, err := store.GetConversation(conv.ID, "default"); err != nil {
		t.Fatalf("refused delete removed the conversation: %v", err)
	}

	out, code := runChatHistory(t, dbPath, "default", "finish", conv.ID, "t1", "--status", "cancelled", "--reason", "stopped", "--exit-code", "137")
	if code != 0 {
		t.Fatalf("finish exit %d: %s", code, out)
	}
	var fin chatFinishResult
	decodeChatJSON(t, out, &fin)
	if !fin.Finalized || fin.Event == nil || fin.Event.Type != chatevents.EventTurnFinished || fin.Event.Seq != 2 || fin.Turn.Status != "cancelled" {
		t.Fatalf("finish = %s", out)
	}
	var payload chatevents.TurnFinishedPayload
	json.Unmarshal(fin.Event.Payload, &payload)
	if payload.ExitCode == nil || *payload.ExitCode != 137 || !payload.HistorySaved {
		t.Errorf("turn.finished payload = %s", fin.Event.Payload)
	}
	out, _ = runChatHistory(t, dbPath, "default", "finish", conv.ID, "t1", "--status", "failed")
	var again chatFinishResult
	decodeChatJSON(t, out, &again)
	if again.Finalized || again.Event != nil || again.Turn.Status != "cancelled" {
		t.Errorf("second finish must be a no-op: %s", out)
	}
	if _, code := runChatHistory(t, dbPath, "default", "finish", conv.ID, "t1", "--status", "active"); code != 3 {
		t.Errorf("finish --status active exit %d, want 3", code)
	}
	if _, code := runChatHistory(t, dbPath, "default", "finish", conv.ID, "nope", "--status", "failed"); code != 2 {
		t.Errorf("finish of an unknown turn exit %d, want 2", code)
	}

	out, code = runChatHistory(t, dbPath, "default", "delete", conv.ID)
	if code != 0 || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("delete exit %d: %s", code, out)
	}
	if _, err := store.GetConversation(conv.ID, "default"); err == nil {
		t.Error("conversation still present after delete")
	}
	if evs, _ := store.GetEvents(conv.ID, "t1", "default", 0, 10); len(evs) != 0 {
		t.Errorf("events survived the delete: %v", evs)
	}
	if _, code := runChatHistory(t, dbPath, "default", "delete", conv.ID); code != 2 {
		t.Errorf("delete of a missing conversation exit %d, want 2", code)
	}
}

func TestChatHistoryReconcileSkipsTheCallersOwnTurns(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	seedProfile(t, dbPath, "work")
	a, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	b, _ := store.CreateConversation("work", "agent", "general", "claude", "", "")
	c, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	store.CreateTurn(a.ID, "default", "old-default", "dead-instance", "hi")
	store.CreateTurn(b.ID, "work", "old-work", "", "hi")
	store.CreateTurn(c.ID, "default", "mine", "me", "hi")
	d, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	store.CreateTurn(d.ID, "default", "done", "dead-instance", "hi")
	store.FinalizeTurn("default", d.ID, "done", chatevents.StatusCompleted, "", nil, true)

	out, code := runChatHistory(t, dbPath, "default", "reconcile", "--except-owner", "me")
	if code != 0 {
		t.Fatalf("reconcile exit %d: %s", code, out)
	}
	var res chatReconcileResult
	decodeChatJSON(t, out, &res)
	if len(res.Reconciled) != 2 || len(res.Errors) != 0 {
		t.Fatalf("reconcile = %s, want the two orphaned turns across both profiles", out)
	}
	for id, want := range map[string]string{"old-default": "interrupted", "mine": "active", "done": "completed"} {
		if got, _ := store.GetTurn(id, "default"); got.Status != want {
			t.Errorf("turn %s = %q, want %q", id, got.Status, want)
		}
	}
	if got, _ := store.GetTurn("old-work", "work"); got.Status != "interrupted" || got.Reason != ai.ReconciledReason {
		t.Errorf("other profile's orphan = %+v", got)
	}
	evs, _ := store.GetEvents(a.ID, "old-default", "default", 0, 10)
	if len(evs) != 1 || evs[0].Type != chatevents.EventTurnFinished {
		t.Errorf("reconcile must journal turn.finished: %+v", evs)
	}
	out, _ = runChatHistory(t, dbPath, "default", "reconcile", "--except-owner", "me")
	if !strings.Contains(out, `"reconciled": []`) || !strings.Contains(out, `"errors": []`) {
		t.Errorf("second reconcile (only the caller's own turn left) = %s", out)
	}
	out, _ = runChatHistory(t, dbPath, "default", "reconcile")
	if !strings.Contains(out, `"id": "mine"`) {
		t.Errorf("reconcile without --except-owner must take every active turn: %s", out)
	}
}

func TestChatHistoryReconcileCleansStaleWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	repo := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(repo, "init", "--quiet")
	git(repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "base")
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, repo)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateTurn(conv.ID, "default", "dead", "dead-instance", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateTurn(other.ID, "default", "mine", "me", "hi"); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(repo, dynorg.WorktreeDirName)
	for _, turn := range []string{"dead", "mine"} {
		git(repo, "worktree", "add", "--quiet", "-b", "monoagent/"+turn+"/w1", filepath.Join(base, turn, "w1"), "HEAD")
	}

	out, code := runChatHistory(t, dbPath, "default", "reconcile", "--except-owner", "me")
	if code != 0 {
		t.Fatalf("reconcile exit %d: %s", code, out)
	}
	var res chatReconcileResult
	decodeChatJSON(t, out, &res)
	if len(res.Worktrees) != 1 || res.Worktrees[0].Turn != "dead" || !res.Worktrees[0].Removed {
		t.Fatalf("worktrees = %+v, want only the dead turn's", res.Worktrees)
	}
	if _, err := os.Stat(filepath.Join(base, "dead")); !os.IsNotExist(err) {
		t.Errorf("the dead turn's worktree is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "mine", "w1")); err != nil {
		t.Errorf("the running turn's worktree is gone: %v", err)
	}
}

// seedProfile adds a profile row, so --profile can resolve it.
func seedProfile(t *testing.T, dbPath, id string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, id); err != nil {
		t.Fatal(err)
	}
}

func TestChatHistoryIsProfileScoped(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	seedProfile(t, dbPath, "work")
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("work", "agent", "general", "claude", "", "")
	store.CreateTurn(conv.ID, "work", "t1", "", "hi")

	out, _ := runChatHistory(t, dbPath, "default", "list")
	if strings.Contains(out, conv.ID) {
		t.Errorf("default profile lists work's conversation: %s", out)
	}
	out, _ = runChatHistory(t, dbPath, "work", "list")
	if !strings.Contains(out, conv.ID) {
		t.Errorf("work profile misses its own conversation: %s", out)
	}
	for _, args := range [][]string{
		{"show", conv.ID}, {"turns", conv.ID}, {"turn", conv.ID, "t1"}, {"events", conv.ID, "t1"},
		{"delete", conv.ID}, {"finish", conv.ID, "t1", "--status", "failed"},
	} {
		if _, code := runChatHistory(t, dbPath, "default", args...); code != 2 {
			t.Errorf("%v from another profile exit %d, want 2 (not found)", args, code)
		}
	}
	if got, _ := store.GetTurn("t1", "work"); got.Status != "active" {
		t.Errorf("another profile's finish touched the turn: %+v", got)
	}
	out, code := runChatHistory(t, dbPath, "work", "create", "--runtime", "claude")
	if code != 0 || !strings.Contains(out, `"profile_id": "work"`) {
		t.Errorf("create under --profile work = %d %s", code, out)
	}
}

func TestChatHistoryTranscriptReadsTheLegacyTable(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin := writeFakeMonomindForChat(t, successTranscript)
	if _, err := runChatCmd(t, dbPath, bin, "--runtime", "fake", "--history-id", "wf-legacy", "hi"); err != nil {
		t.Fatal(err)
	}
	out, code := runChatHistory(t, dbPath, "default", "transcript", "wf-legacy")
	if code != 0 {
		t.Fatalf("transcript exit %d", code)
	}
	var got struct {
		Items []ai.ChatMessage `json:"items"`
	}
	decodeChatJSON(t, out, &got)
	if len(got.Items) != 2 || got.Items[0].Role != "user" || got.Items[1].Content != "hello from the fake runtime" {
		t.Errorf("transcript = %s", out)
	}
	out, _ = runChatHistory(t, dbPath, "default", "transcript", "none")
	if !strings.Contains(out, `"items": []`) {
		t.Errorf("empty transcript = %s", out)
	}
}

// `chat history refused` records why a turn never began, and `chat history turns` then shows that reason, so the
// failure is visible later in the terminal and when the conversation is reopened.
func TestChatHistoryRefusedRecordsAReasonTurnsShows(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--model", "sonnet")
	if code != 0 {
		t.Fatalf("create exit %d: %s", code, out)
	}
	var conv ai.ConversationRecord
	decodeChatJSON(t, out, &conv)
	if out, code := runChatHistory(t, dbPath, "default", "refused", conv.ID, "--code", "agent_not_setup", "--reason", "the host refused the turn"); code != 0 {
		t.Fatalf("refused exit %d: %s", code, out)
	}
	out, code = runChatHistory(t, dbPath, "default", "turns", conv.ID)
	if code != 0 {
		t.Fatalf("turns exit %d: %s", code, out)
	}
	var page struct {
		Items []ai.TurnRecord `json:"items"`
	}
	decodeChatJSON(t, out, &page)
	if len(page.Items) != 1 || page.Items[0].Status != "failed" || page.Items[0].Reason != "the host refused the turn" {
		t.Fatalf("turns: %s", out)
	}
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	text := captureStdout(t, func() {
		cmd := newChatCmd(cfg)
		cmd.SetArgs([]string{"history", "turns", conv.ID})
		_ = cmd.Execute()
	})
	if !strings.Contains(text, "the host refused the turn") {
		t.Fatalf("plain turns output hides the reason: %q", text)
	}
}

// The verb is the operator's (an agent forging a host refusal is refused before anything is written), takes only a
// known code, and prints a stored reason without control characters.
func TestChatHistoryRefusedIsTheOperatorsAndTakesKnownCodes(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	out, _ := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--model", "sonnet")
	var conv ai.ConversationRecord
	decodeChatJSON(t, out, &conv)
	agent := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	agent.OperatorRefusal = func() string { return "an agent" }
	agent.RequireOperator = func(what string) error { return clicore.OperatorOnly("only the user can %s", what) }
	cmd := newChatCmd(agent)
	cmd.SetArgs([]string{"history", "refused", conv.ID, "--reason", "forged"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); exitCodeFor(err) != 3 {
		t.Fatalf("an agent's refused: %v", err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "refused", conv.ID, "--code", "doomsday.stopped", "--reason", "x"); code == 0 {
		t.Error("accepted a notice code of another family")
	}
	if _, code := runChatHistory(t, dbPath, "default", "refused", conv.ID, "--reason", "evil\x1b[2Jtext"); code != 0 {
		t.Fatal("refused a plain reason")
	}
	text := captureStdout(t, func() {
		c := newChatCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}))
		c.SetArgs([]string{"history", "turns", conv.ID})
		_ = c.Execute()
	})
	if strings.Contains(text, "\x1b") || !strings.Contains(text, "reason: evil[2Jtext") {
		t.Fatalf("turns output: %q", text)
	}
}

// A recorded refusal is not the boss's first turn: the first turn can still be retried after the host refused it.
func TestARefusalIsNotATurnForTheBossFirstTurn(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	out, _ := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--model", "sonnet")
	var conv ai.ConversationRecord
	decodeChatJSON(t, out, &conv)
	if _, code := runChatHistory(t, dbPath, "default", "refused", conv.ID, "--reason", "the host refused"); code != 0 {
		t.Fatal("refused")
	}
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	if had, err := bossHadATurn(cfg, "default", conv.ID); err != nil || had {
		t.Fatalf("bossHadATurn after a refusal: %v %v", had, err)
	}
	db, _ := cfg.OpenDB()
	defer db.Close()
	cs := coderhost.NewStore(db.DB)
	if _, _, err := cs.EnqueueSendBounded(coderhost.Command{ProfileID: "default", ConversationID: conv.ID, Kind: coderhost.KindSend, TurnID: "t-1", Message: "go"}, coderhost.SendBounds{MaxPending: 1, NoTurnYet: true}); err != nil {
		t.Fatalf("the first turn after a refusal: %v", err)
	}
}
