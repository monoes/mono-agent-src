package aitask

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/operator"
)

// fakeSelf stands in for monoagentcli: a script that logs its argv and
// stdin, and answers `chat history create` and `chat --conversation`.
func fakeSelf(t *testing.T, turnScript string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	script := "#!/bin/sh\n" +
		`echo "$*" >> '` + log + "'\n" +
		"case \"$*\" in\n" +
		`*"chat history create"*) env >> '` + log + `.env'; echo '{"id":"conv-7","runtime_id":"claude","cwd":"` + dir + `"}'; exit 0;;` + "\n" +
		`*"chat --conversation"*) cat > '` + log + `.prompt'; echo "task=$MONOAGENT_AITASK_ID" >> '` + log + "'\n" + turnScript + "\n;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := selfPath
	selfPath = func() (string, error) { return bin, nil }
	t.Cleanup(func() { selfPath = old })
	return log
}

func runningTask(t *testing.T, d *Deps) (*aitask.Service, func(), aitask.Task) {
	t.Helper()
	svc, closeDB, err := (&cli{d: d}).open()
	if err != nil {
		t.Fatal(err)
	}
	task, err := svc.Store.Insert(aitask.Task{ID: aitask.NewID(), Kind: "generic", ConversationID: "conv-7", Status: aitask.StatusRunning,
		Goal: "g", Prompt: "the first turn"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, closeDB, task
}

func TestCreateConversationReadsChatHistoryCreate(t *testing.T) {
	log := fakeSelf(t, "exit 0")
	d := deps(t, nil)
	conv, err := (selfLauncher{d}).CreateConversation(context.Background(), "codex", "draft-reply", "qa")
	if err != nil || conv.ID != "conv-7" || conv.Runtime != "claude" || conv.Cwd == "" {
		t.Fatalf("%+v %v", conv, err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "chat history create --mode coder --profile-folder --runtime codex") || !strings.Contains(string(b), "--profile=p1") {
		t.Fatalf("argv = %s", b)
	}
}

// A turn that ends without `complete` leaves the task needs_user, with the
// error when the turn failed.
func TestSupervisedTurnThatEndsWithoutCompleteNeedsTheUser(t *testing.T) {
	for name, tc := range map[string]struct{ script, wantErr string }{
		"clean exit": {"exit 0", ""},
		"failed":     {"echo boom >&2; exit 3", "ended with an error"},
	} {
		log := fakeSelf(t, tc.script)
		d := deps(t, nil)
		svc, closeDB, task := runningTask(t, d)
		t.Setenv(aitask.EnvTaskID, task.ID)
		cmd := New(*d)
		cmd.SetArgs([]string{"supervise", task.ID})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := svc.Store.Get(task.ID)
		if got.Status != aitask.StatusNeedsUser || (tc.wantErr == "") != (got.Error == "") || !strings.Contains(got.Error, tc.wantErr) {
			t.Errorf("%s: %+v", name, got)
		}
		if p, _ := os.ReadFile(log + ".prompt"); string(p) != "the first turn" {
			t.Errorf("%s: the turn got prompt %q", name, p)
		}
		if b, _ := os.ReadFile(log); !strings.Contains(string(b), "chat --conversation conv-7 --turn ") || !strings.Contains(string(b), "--prompt-stdin") {
			t.Errorf("%s: argv = %s", name, b)
		}
		closeDB()
	}
}

func TestSupervisedTurnStopsWhenTheTaskIsCancelled(t *testing.T) {
	fakeSelf(t, "sleep 30")
	d := deps(t, nil)
	svc, closeDB, task := runningTask(t, d)
	defer closeDB()
	t.Setenv(aitask.EnvTaskID, task.ID)
	done := make(chan error, 1)
	go func() {
		cmd := New(*d)
		cmd.SetArgs([]string{"supervise", task.ID})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		done <- cmd.Execute()
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := svc.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn kept running after the task was cancelled")
	}
	if got, _ := svc.Store.Get(task.ID); got.Status != aitask.StatusCancelled {
		t.Fatalf("status = %s", got.Status)
	}
}

// Detach starts a supervisor with the task's id and token in its environment.
func TestDetachGivesTheSupervisorTheTaskEnvironment(t *testing.T) {
	log := fakeSelf(t, "exit 0")
	d := deps(t, nil)
	script := `#!/bin/sh
echo "$* id=$MONOAGENT_AITASK_ID token=$MONOAGENT_AITASK_TOKEN" >> '` + log + `'
`
	bin := filepath.Dir(log) + "/monoagentcli"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (selfLauncher{d}).Detach(context.Background(), "at-1", "tok-xyz"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(log); strings.Contains(string(b), "aitask supervise at-1 id=at-1 token=tok-xyz") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(log)
	t.Fatalf("the supervisor did not start with the task env: %s", b)
}

// `chat history create` above scoped is the operator's now, and the launcher
// runs it for `submit` (operator-only itself): the child it starts carries no
// agent, chat or workflow marker of its own, so it passes the check.
func TestCreateConversationChildCarriesNoOperatorMarker(t *testing.T) {
	log := fakeSelf(t, "exit 0")
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	d := deps(t, nil)
	if _, err := (selfLauncher{d}).CreateConversation(context.Background(), "", "draft-reply", "qa"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log + ".env")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		for _, k := range operator.MarkerEnv() {
			if v, ok := strings.CutPrefix(line, k+"="); ok && v != "" {
				t.Errorf("the child carries %s=%s, so `chat history create` would be refused as not the operator's", k, v)
			}
		}
	}
}

// Each level a task can ask for becomes a real conversation level; a confined
// kind keeps the coder conversation (its turns are narrowed to its own profile),
// and qa on any other kind is refused with no conversation made.
func TestCreateConversationAppliesTheTaskAccess(t *testing.T) {
	for _, tc := range []struct {
		name, kind, access, want, refuse string
	}{
		{"coding", "generic", "coding", "chat history create --access coding --profile-folder", ""},
		{"workspace", "generic", "workspace", "chat history create --access workspace --profile-folder", ""},
		{"qa generic is refused", "generic", "qa", "", "access_unenforceable"},
		{"empty access is refused", "generic", "", "", "access_unenforceable"},
		{"confined qa unchanged", "draft-reply", "qa", "chat history create --mode coder --profile-folder", ""},
		{"confined workspace unchanged", "workflow-generate", "workspace", "chat history create --mode coder --profile-folder", ""},
		{"apply unchanged", "application-apply", "coding", "chat history create --mode coder --profile-folder", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := fakeSelf(t, "exit 0")
			_, err := (selfLauncher{deps(t, nil)}).CreateConversation(context.Background(), "", tc.kind, tc.access)
			b, _ := os.ReadFile(log)
			if tc.refuse != "" {
				var ae *aitask.Error
				if !errors.As(err, &ae) || ae.Code != tc.refuse {
					t.Fatalf("err = %v, want %s", err, tc.refuse)
				}
				if len(b) != 0 {
					t.Fatalf("a refused task still ran the CLI: %s", b)
				}
				return
			}
			if err != nil || !strings.Contains(string(b), tc.want) {
				t.Fatalf("err = %v, argv = %s, want %q", err, b, tc.want)
			}
		})
	}
}

// The bound that matters: over every kind and level, a conversation with a
// shell (anything but the confined mode) is never made for qa. Dropping the
// switch's refusal (or the confined-kind test) fails this for every pair.
func TestNoUnconfinedQAConversationForAnyKind(t *testing.T) {
	made := 0
	for _, k := range aitask.All() {
		for _, a := range []string{aitask.AccessQA, ""} {
			args, err := conversationLevel(k.Name, a)
			if err == nil {
				made++
				if !chatcli.ConfinedKind(k.Name) {
					t.Errorf("kind %s at %q got a conversation (%v) without being confined", k.Name, a, args)
				}
			}
		}
	}
	if made == 0 {
		t.Fatal("no confined kind is registered here: the test checked nothing")
	}
}

// The refusal only suggests levels the kind allows, and Enforceable agrees
// with the creation rule (start uses it).
func TestEnforceableAndRefusalMessage(t *testing.T) {
	l := selfLauncher{deps(t, nil)}
	if err := l.Enforceable("generic", "qa"); err == nil {
		t.Fatal("generic at qa is enforceable")
	}
	if err := l.Enforceable("draft-reply", "qa"); err != nil {
		t.Fatal(err)
	}
	err := l.Enforceable("generic", "qa")
	if !strings.Contains(err.Error(), "coding or workspace") {
		t.Fatalf("message = %v", err)
	}
	aitask.Register(aitask.Kind{Name: "qa-only-test", DefaultAccess: aitask.AccessQA})
	err = l.Enforceable("qa-only-test", "qa")
	if err == nil || strings.Contains(err.Error(), "--access coding") {
		t.Fatalf("a qa-ceiling kind was told to raise: %v", err)
	}
}
