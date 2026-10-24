package aitask

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/storage"
)

const goodDraft = `{"summary":"s","draft":{"body":"Friday works."},"assumptions":[]}`

func submitKind(t *testing.T, kind string) (*Deps, string, *tokenFake) {
	t.Helper()
	f := &tokenFake{fake: fake{cwd: t.TempDir()}}
	d := deps(t, func() aitask.Launcher { return f })
	unlockOnCleanup(t, d)
	out, err := run(t, d, "submit", "--kind", kind, "--goal", "g")
	if err != nil {
		t.Fatal(err)
	}
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatal(err)
	}
	t.Setenv(aitask.EnvTaskID, task.TaskID)
	t.Setenv(aitask.EnvToken, f.token)
	return d, task.TaskID, f
}

// A confined task cannot use --result to probe the user's files: a path outside
// its work folder is refused before anything is read, and nothing of the file
// reaches the error.
func TestConfinedTaskResultMustLiveInItsWorkFolder(t *testing.T) {
	for _, kind := range []string{"draft-reply", "workflow-generate", "failed-run-fix", "recording-analyze", "extension-fill", "extension-ask"} {
		d, id, _ := submitKind(t, kind)
		secrets := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(secrets, []byte(`{"private_key_id":"TOPSECRETKEYID","client_email":"x@y"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := run(t, d, "complete", id, "--result", secrets)
		if err == nil || !strings.Contains(err.Error(), "not under this task's work folder") {
			t.Fatalf("%s: a result outside the work folder was read: %v", kind, err)
		}
		if strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "private_key_id") {
			t.Errorf("%s: the error echoes the file: %v", kind, err)
		}
		// A link inside the work folder to a file outside is outside too.
		work := filepath.Join(filepath.Dir(d.DBPath()), "aitask", id, "work")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(work, "r.json")
		if err := os.Symlink(secrets, link); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, d, "complete", id, "--result", link); err == nil || !strings.Contains(err.Error(), "not under this task's work folder") {
			t.Errorf("%s: a link out of the work folder was followed: %v", kind, err)
		}
		// ".." out of it too.
		if _, err := run(t, d, "complete", id, "--result", filepath.Join(work, "..", "..", "..", filepath.Base(filepath.Dir(secrets)), "credentials.json")); err == nil {
			t.Errorf("%s: a .. path left the work folder", kind)
		}
	}
}

func TestConfinedTaskMayHandBackFromItsWorkFolderOrInline(t *testing.T) {
	d, id, _ := submitKind(t, "draft-reply")
	if out, err := run(t, d, "complete", id, "--result", writeResult(t, d, id, goodDraft)); err != nil || !strings.Contains(out, `"status":"complete"`) {
		t.Fatalf("a result from the work folder: %v %s", err, out)
	}
	d, id, _ = submitKind(t, "draft-reply")
	if out, err := run(t, d, "complete", id, "--result-json", goodDraft); err != nil || !strings.Contains(out, `"status":"complete"`) {
		t.Fatalf("an inline result: %v %s", err, out)
	}
	d, id, _ = submitKind(t, "draft-reply")
	cmd := New(*d)
	cmd.SetIn(strings.NewReader(goodDraft))
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"complete", id, "--result", "-"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil || !strings.Contains(buf.String(), `"status":"complete"`) {
		t.Fatalf("a result on stdin: %v %s", err, buf.String())
	}
}

func TestCompleteTakesExactlyOneResultSource(t *testing.T) {
	d, id, _ := submitKind(t, "draft-reply")
	if _, err := run(t, d, "complete", id); err == nil {
		t.Error("no result source accepted")
	}
	if _, err := run(t, d, "complete", id, "--result", "x.json", "--result-json", goodDraft); err == nil {
		t.Error("two result sources accepted")
	}
	if _, err := run(t, d, "complete", id, "--result-json", `{"summary":`); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("broken inline JSON: %v", err)
	}
}

// Other kinds keep reading a result from anywhere.
func TestGenericTaskResultFileIsNotRestricted(t *testing.T) {
	d, id, _ := submitKind(t, "generic")
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(`{"summary":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, d, "complete", id, "--result", p); err != nil || !strings.Contains(out, `"status":"complete"`) {
		t.Fatalf("%v %s", err, out)
	}
}

// A link under the work folder whose target does not exist is not inside it.
func TestResultPathRefusesADanglingLinkInTheWorkFolder(t *testing.T) {
	d, id, _ := submitKind(t, "draft-reply")
	work := filepath.Join(filepath.Dir(d.DBPath()), "aitask", id, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "r.json")
	if err := os.Symlink(filepath.Join(t.TempDir(), "not-yet"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, d, "complete", id, "--result", link); err == nil || !strings.Contains(err.Error(), "not under this task's work folder") {
		t.Fatalf("a dangling link was treated as inside: %v", err)
	}
}

// When the task cannot be looked up the path is refused, never read.
func TestResultPathFailsClosedWhenTheTaskCannotBeLooked(t *testing.T) {
	d, id, _ := submitKind(t, "draft-reply")
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(goodDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := *d
	broken.OpenDB = func() (*storage.Database, error) { return nil, errors.New("no database") }
	c := &cli{d: &broken}
	if _, err := c.resultPathAllowed(id, p); err == nil {
		t.Fatal("a result path was allowed with the task unreadable")
	}
	if _, err := (&cli{d: d}).resultPathAllowed("at-0000000000000000", p); err == nil {
		t.Fatal("a result path was allowed for an unknown task")
	}
}

// tokenFake is the fake launcher that also keeps the token it is given, so a
// test can answer as the task's own session.
type tokenFake struct {
	fake
	token string
}

func (f *tokenFake) Detach(_ context.Context, id, token string) error {
	f.token = token
	return f.fake.Detach(context.Background(), id, token)
}

// writeResult puts body in task id's own work folder, the one place a
// confined task may hand a result file back from.
func writeResult(t *testing.T, d *Deps, id, body string) string {
	t.Helper()
	work := filepath.Join(filepath.Dir(d.DBPath()), "aitask", id, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(work, "result.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
