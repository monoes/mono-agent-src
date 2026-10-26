package chat

import (
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
)

func TestMasterAnswersAChildsQuestionWithTheSessionTokenAndNothingElse(t *testing.T) {
	m := newMasterTurn(t)
	var spawned struct{ ConversationID string }
	out, code := runSessionsCLI(t, m.cfg, "spawn")
	if code != 0 {
		t.Fatalf("spawn: %d %s", code, out)
	}
	decodeChatJSON(t, out, &spawned)
	kid := spawned.ConversationID
	cs := openCoder(t, m.dbPath)
	q, err := cs.Park("default", kid, "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// a stranger session with its own parked question
	store := openTestChatStore(t, m.dbPath)
	other, _ := store.CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessWorkspace, t.TempDir(), "")
	qo, err := cs.Park("default", other.ID, "risky_action", "not yours", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	before := len(pendingCommands(t, m.dbPath))

	var texts []string
	for _, args := range [][]string{
		{"questions", "answer", other.ID, qidStr(qo.ID), "yes", "--no-host"}, // an unrelated session's question
		{"questions", "answer", kid, qidStr(qo.ID), "yes", "--no-host"},      // its id under my child
		{"questions", "answer", kid, "9999", "yes", "--no-host"},             // unknown
	} {
		out, code := runSessionsCLI(t, m.cfg, args...)
		if code == 0 || !strings.Contains(out, "not one of your child sessions") {
			t.Fatalf("%v: exit %d %s", args, code, out)
		}
		texts = append(texts, out)
	}
	if len(pendingCommands(t, m.dbPath)) != before || cs.OpenParked("default", other.ID) != 1 {
		t.Fatal("a refused answer changed something")
	}

	out, code = runSessionsCLI(t, m.cfg, "questions", "answer", kid, qidStr(q.ID), "no, ask first", "--no-host")
	if code != 0 {
		t.Fatalf("master answer: %d %s", code, out)
	}
	got, _ := cs.GetQuestion("default", q.ID)
	if got.AnsweredBy != "master:"+m.master {
		t.Fatalf("answered by %q", got.AnsweredBy)
	}
	var answers int
	for _, c := range pendingCommands(t, m.dbPath) {
		if strings.HasPrefix(c.TurnID, coderhost.AnswerPrefix) {
			answers++
			if !strings.HasPrefix(c.TurnID, "answer-m-") || c.Issuer != m.master || c.WantsMission {
				t.Errorf("answer command = %+v", c)
			}
		}
	}
	if answers != 1 {
		t.Fatalf("answers = %d", answers)
	}

	// a token that does not verify is a refusal, never the operator path
	t.Setenv(coderhost.SessionTokenEnv, "mst-forged")
	if _, code := runSessionsCLI(t, m.cfg, "questions", "answer", kid, qidStr(q.ID), "x", "--no-host"); code == 0 {
		t.Fatal("a forged token answered")
	}
}

func qidStr(id int64) string { return strconv.FormatInt(id, 10) }
