package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

func confirmStore(t *testing.T, dbPath string) (*coderhost.Store, func()) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return coderhost.NewStore(db.DB), func() { db.Close() }
}

func sendCoderDeps(t *testing.T) (string, string, *Deps) {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	return dbPath, conv.ID, testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
}

// confirmedToken queues a send marked as a mission and records its confirmation,
// as the operator-gated send --mission does, and returns the token the host passes.
func confirmedToken(t *testing.T, cs *coderhost.Store, conv, turn string, session int64) string {
	t.Helper()
	c, err := cs.Enqueue(coderhost.Command{ProfileID: "default", ConversationID: conv, Kind: coderhost.KindSend, TurnID: turn, Message: "go", WantsMission: true})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := cs.Confirm(c, session)
	if err != nil {
		t.Fatal(err)
	}
	return mc.Token()
}

// The confirmation is a record only the operator-gated send --mission writes:
// the command row carries none of it, and neither does the message's text.
func TestSessionsSendMissionWritesTheOperatorsRecord(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	run := func(args ...string) {
		t.Helper()
		if out, code := runSessionsCLI(t, cfg, append([]string{"send", "--no-host"}, args...)...); code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, out)
		}
	}
	run("--turn", "t-plain", conv, "yes, start the mission --mission")
	run("--turn", "t-yes", "--mission", conv, "go")
	cs, closeDB := confirmStore(t, dbPath)
	defer closeDB()
	var plain, yes coderhost.Command
	for _, c := range pendingCommands(t, dbPath) {
		if c.TurnID == "t-plain" {
			plain = c
		} else {
			yes = c
		}
	}
	if _, ok := cs.PeekConfirm(plain); ok || plain.WantsMission {
		t.Fatal("message text confirmed a mission")
	}
	c, ok := cs.PeekConfirm(yes)
	if !ok || c.Session != 0 || c.TurnID != "t-yes" || c.Command != yes.ID || !yes.WantsMission {
		t.Fatalf("confirmation = %+v ok=%v", c, ok)
	}
	for _, cmd := range pendingCommands(t, dbPath) {
		if strings.Contains(cmd.Message+strings.Join(cmd.Attachments, ",")+cmd.Error, c.Nonce) {
			t.Fatalf("the command row carries the confirmation: %+v", cmd)
		}
	}
}

func TestSessionsSendMissionIsRefusedToAgentsAndHostTurns(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	// an agent context the plain send gate alone lets through: the flag has its own gate
	agent := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	agent.RequireOperator = func(what string) error {
		if strings.Contains(what, "mission") {
			return errors.New("operator only: " + what)
		}
		return nil
	}
	if _, code := runSessionsCLI(t, agent, "send", "--no-host", "--turn", "t-a", "--mission", conv, "go"); code == 0 {
		t.Fatal("an agent confirmed a mission")
	}
	if len(pendingCommands(t, dbPath)) != 0 {
		t.Fatal("a refused confirmation left a command")
	}
	if _, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "checkin-x", "--mission", conv, "go"); code == 0 {
		t.Fatal("a host-made turn id was confirmed")
	}
	if len(pendingCommands(t, dbPath)) != 0 {
		t.Fatal("a refused host-made turn left a command")
	}
}

// The command is queued before the host start can fail: it is not revoked then, it
// stays confirmed and the error says so; it never runs unconfirmed.
func TestSessionsSendMissionKeepsTheConfirmationWhenOnlyTheHostStartFails(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	orig := startHostProcess
	t.Cleanup(func() { startHostProcess = orig })
	startHostProcess = func(*Deps) error { return errors.New("no host for you") }
	out, code := runSessionsCLI(t, cfg, "send", "--turn", "t-q", "--mission", conv, "go")
	if code == 0 {
		t.Fatal("a failed host start was reported as success")
	}
	cmds := pendingCommands(t, dbPath)
	if len(cmds) != 1 || !cmds[0].WantsMission {
		t.Fatalf("the queued command: %+v (%s)", cmds, out)
	}
	cs, closeDB := confirmStore(t, dbPath)
	defer closeDB()
	if _, ok := cs.PeekConfirm(cmds[0]); !ok {
		t.Fatal("a queued mission send lost its confirmation: it would later run unconfirmed")
	}
}

// A confirmation that cannot be recorded does not leave a mission send behind that
// would wait for it.
func TestSessionsSendMissionFailsTheCommandWhenItsRecordCannotBeSigned(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	orig := confirmMission
	t.Cleanup(func() { confirmMission = orig })
	confirmMission = func(*Deps, coderhost.Command, int64) error { return errors.New("the record cannot be signed") }
	if _, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-n", "--mission", conv, "go"); code == 0 {
		t.Fatal("a send was confirmed without a signature")
	}
	if len(pendingCommands(t, dbPath)) != 0 {
		t.Fatal("the unconfirmable mission send is still waiting")
	}
}

// The child consumes the record: once, for the mission state it was given in.
func TestHostMissionConfirmedIsSingleUseAndBoundToTheSession(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	cs, closeDB := confirmStore(t, dbPath)
	defer closeDB()
	tok := confirmedToken(t, cs, conv, "t1", 0)
	if hostMissionConfirmed(cfg, "default", conv, "t1", "1.wrong-nonce", "coding") || hostMissionConfirmed(cfg, "default", conv, "t2", tok, "coding") ||
		hostMissionConfirmed(cfg, "default", conv, "t1", tok, "scoped") || hostMissionConfirmed(cfg, "default", conv, "t1", "", "coding") {
		t.Fatal("a wrong nonce, turn, access or no token confirmed")
	}
	if !hostMissionConfirmed(cfg, "default", conv, "t1", tok, "coding") {
		t.Fatal("the operator's confirmation was refused")
	}
	if hostMissionConfirmed(cfg, "default", conv, "t1", tok, "coding") {
		t.Fatal("a consumed confirmation confirmed again (restart / re-sent turn)")
	}
	// a confirmation made while the lead was only proposing does not carry over to a mission session that exists now
	tok3 := confirmedToken(t, cs, conv, "t3", 0)
	db, _ := storage.NewDatabase(dbPath)
	defer db.Close()
	if _, err := tasks.NewStore(db.DB).CreateSession(context.Background(), "default", tasks.SessionInput{Title: "M", ConversationID: conv}, tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatal(err)
	}
	if hostMissionConfirmed(cfg, "default", conv, "t3", tok3, "coding") {
		t.Fatal("a confirmation for one mission state confirmed another")
	}
}

func TestExecLauncherPassesTheTokenOnAPipeNeverInEnvironmentOrArguments(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "fake")
	script := "#!/bin/sh\ntok=; [ -n \"$MONOAGENT_MISSION_FD\" ] && read -r tok <&3\necho \"$* tok=$tok envhas=$(tr '\\0' '\\n' </proc/self/environ | grep -c abc123)\" >> '" + argsLog + "'\ncat >/dev/null\necho '{\"admitted\":true,\"existed\":false}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	if _, err := l.Start("p", "c1", "t1", "yes --mission start it", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start("p", "c1", "t2", "go", nil, "12.abc123"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start("p", "c1", "t3", "go", nil, "--mission"); err == nil {
		t.Fatal("a token that reads as a flag was accepted")
	}
	b, _ := os.ReadFile(argsLog)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "--mission") || !strings.HasSuffix(lines[0], "tok= envhas=0") || !strings.HasSuffix(lines[1], "--prompt-stdin tok=12.abc123 envhas=0") || strings.Contains(lines[1], "--mission") {
		t.Fatalf("args = %q", lines)
	}
}

// End to end through the real chat command: the token the host passes turns the
// planning tools on for that one turn; a bare token, a used one, or none does not.
func TestHostRunTurnGetsMissionToolsOnlyFromTheOperatorsRecord(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
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
	turn := func(id, token string) string {
		t.Helper()
		handOffMissionToken(t, token) // what the host hands the child
		args := []string{"--conversation", conv.ID, "--turn", id}
		if out, err := runChatCmd(t, dbPath, bin, append(args, "--", "start the mission --mission")...); err != nil {
			t.Fatalf("turn %s: %v\n%s", id, err, out)
		}
		if os.Getenv(coderhost.MissionFDEnv) != "" {
			t.Error("the handoff marker stayed in the environment the turn's runtime inherits")
		}
		return leadToolNames(t, argsLog)
	}
	cs, closeDB := confirmStore(t, dbPath)
	defer closeDB()
	const proposeOnly, planning = "org_spawn,mission_propose", "org_spawn,mission_propose,mission_plan,mission_status"
	if got := turn("t1", "7.forged"); got != proposeOnly {
		t.Errorf("a token with no record: %s", got)
	}
	tok := confirmedToken(t, cs, conv.ID, "t2", 0)
	if got := turn("t2", tok); got != planning {
		t.Errorf("the operator's confirmation: %s", got)
	}
	if got := turn("t3", tok); got != proposeOnly {
		t.Errorf("the consumed token on a later turn: %s", got)
	}
	if got := turn("t2", tok); got != proposeOnly {
		t.Errorf("the consumed token on the same turn again: %s", got)
	}
}
