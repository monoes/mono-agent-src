//go:build !windows

package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

const (
	planItem    = `INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('default','c1','plan_approval',5,'x')`
	draftPlan   = `INSERT INTO task_sessions (id, profile_id, conversation_id, title, state, kind, created_by, created_at, updated_at) VALUES (5, 'default', 'c1', 't', 'draft', 'mission', 'you', 'x', 'x')`
	stoppedChat = `INSERT INTO coder_sessions (profile_id, conversation_id, state, created_at, updated_at) VALUES ('default','c1','stopped','x','x')`
)

// #1663: an item that is not open any more (its session is stopped, its question answered, the row it points at gone) does not hold
// a restart; one that is open still does.
func TestOnlyOpenPersonItemsHoldTheRestart(t *testing.T) {
	stale := map[string][]string{
		"a plan of a stopped session":   {draftPlan, planItem, stoppedChat},
		"an item whose row is gone":     {planItem},
		"a plan that is no longer open": {strings.Replace(draftPlan, "'draft'", "'running'", 1), planItem},
		"an answered parked question": {
			`INSERT INTO coder_session_questions (id, profile_id, conversation_id, class, status, created_at) VALUES (3,'default','c1','risky_action','answered','x')`,
			`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('default','c1','parked_question',3,'x')`},
	}
	for name, stmts := range stale {
		cfg, stopped := staleHostFixture(t)
		for _, st := range stmts {
			execOn(t, cfg, st)
		}
		if err := ensureCurrentHost(cfg); err != nil || *stopped != 1 {
			t.Errorf("%s blocks the restart: err=%v stops=%d", name, err, *stopped)
		}
	}
	live := map[string][]string{
		"a plan of a live session": {draftPlan, planItem},
		"a plan of an idle session": {draftPlan, planItem,
			`INSERT INTO coder_sessions (profile_id, conversation_id, state, created_at, updated_at) VALUES ('default','c1','idle','x','x')`},
		"a plan of another profile": {strings.ReplaceAll(draftPlan, "'default'", "'other'"), strings.ReplaceAll(planItem, "'default'", "'other'")},
	}
	for name, stmts := range live {
		cfg, stopped := staleHostFixture(t)
		for _, st := range stmts {
			execOn(t, cfg, st)
		}
		if err := ensureCurrentHost(cfg); err == nil || *stopped != 0 {
			t.Errorf("%s must still block: err=%v stops=%d", name, err, *stopped)
		}
	}
}

// #1663: a question id that is not one of the conversation's is refused before the host is touched.
func TestAnswerWithAnInvalidQuestionIdDoesNotRestartTheHost(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	store := openTestChatStore(t, cfg.DBPath())
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	for _, id := range []string{"999", "0", "-1", "x"} {
		if _, code := runSessionsCLI(t, cfg, "questions", "answer", conv.ID, id, "yes"); code == 0 {
			t.Fatalf("question %s was accepted", id)
		}
		if *stopped != 0 {
			t.Fatalf("question %s restarted the host", id)
		}
	}
}

// #1663 (review): a question that is already answered is refused before the host is touched.
func TestAnswerOfASettledQuestionDoesNotRestartTheHost(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	store := openTestChatStore(t, cfg.DBPath())
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	execOn(t, cfg, fmt.Sprintf(`INSERT INTO coder_session_questions (id, profile_id, conversation_id, class, status, created_at) VALUES (4,'default','%s','ambiguity','answered','x')`, conv.ID))
	if _, code := runSessionsCLI(t, cfg, "questions", "answer", conv.ID, "4", "yes"); code == 0 || *stopped != 0 {
		t.Fatalf("code=%d stops=%d", code, *stopped)
	}
}
