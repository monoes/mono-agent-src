package finish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rig struct {
	t    *testing.T
	snap string
	tree Tree
	sb   Sandbox
	// placed counts the commands handed to Place, and whether each was still unstarted.
	placed    int
	startedAt []bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	root := t.TempDir()
	write(t, root, "a.txt", "alpha", 0o644)
	write(t, root, "tool.sh", "#!/bin/sh\necho tool\n", 0o755)
	r := &rig{t: t}
	r.snap = filepath.Join(t.TempDir(), "snap")
	tree, err := Snapshot(root, r.snap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { RemoveTree(r.snap) })
	r.tree = tree
	scratch := t.TempDir()
	r.sb = Sandbox{Scratch: scratch, Place: func(cmd *exec.Cmd) (func(), error) {
		r.placed++
		r.startedAt = append(r.startedAt, cmd.Process != nil)
		return func() {}, nil
	}}
	return r
}

func (r *rig) run(kind, spec, expected string) Measured {
	r.t.Helper()
	p, err := ParsePlan(kind, spec, expected)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.sb.Run(context.Background(), r.snap, r.tree, p)
}

func TestCriterionPassFailAndExactOutput(t *testing.T) {
	r := newRig(t)
	if o := r.run("command", "echo hello", "hello"); o.Result != ResPass || o.StdoutDigest == "" || !o.RunsSubjectCode {
		t.Fatalf("%+v", o)
	}
	if r.placed != 1 || r.startedAt[0] {
		t.Fatalf("the program was not placed before it started: %v %v", r.placed, r.startedAt)
	}
	if o := r.run("command", "echo hello", "hellO"); o.Result != ResFail { // one byte
		t.Fatalf("%+v", o)
	}
	if o := r.run("command", `printf 'hello\n\n'`, "hello"); o.Result != ResFail { // one extra newline is a difference
		t.Fatalf("%+v", o)
	}
	if o := r.run("command", "false", ""); o.Result != ResFail || o.ExitCode != 1 {
		t.Fatalf("%+v", o)
	}
	if o := r.run("command", "false", "exit:0,1"); o.Result != ResPass {
		t.Fatalf("%+v", o)
	}
	if o := r.run("command", "cat a.txt", "alpha"); o.Result != ResPass { // runs in the working copy
		t.Fatalf("%+v", o)
	}
}

func TestResultFileTheChildWritesIsNeverRead(t *testing.T) { // T23, R6
	r := newRig(t)
	o := r.run("command", `sh -c 'echo PASS > /dev/null; echo ok > ../scratch/tmp/result.json; exit 3'`, "")
	if o.Result != ResFail || o.ExitCode != 3 {
		t.Fatalf("a failing program passed: %+v", o)
	}
}

func TestTimeoutKillsTheProgramAndOutputCapStopsIt(t *testing.T) { // T29
	r := newRig(t)
	r.sb.After = func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Time{}; return c }
	start := time.Now()
	if o := r.run("command", "sleep 30", ""); o.Result != ResTimeout || time.Since(start) > 10*time.Second {
		t.Fatalf("%+v after %v", o, time.Since(start))
	}
	r2 := newRig(t)
	r2.sb.OutCap = 100
	if o := r2.run("command", "yes", ""); o.Result != ResOversize || len(o.Stdout) > 100 {
		t.Fatalf("%+v", o)
	}
	if o := r2.run("output", "yes", "y"); o.Result != ResOversize { // truncation never passes an output criterion
		t.Fatalf("%+v", o)
	}
}

func TestAProgramThatWritesIntoTheSubjectIsTainted(t *testing.T) { // T24
	r := newRig(t)
	o := r.run("command", `sh -c 'chmod u+w . && echo x > planted'`, "")
	if o.Result != ResTainted || len(o.Tainted) == 0 || o.Tainted[0] != "planted" {
		t.Fatalf("%+v", o)
	}
	// the sealed snapshot itself is untouched, and a later criterion sees the original subject
	if o := r.run("command", "ls", "a.txt\ntool.sh"); o.Result != ResPass {
		t.Fatalf("%+v", o)
	}
}

func TestEnvironmentIsBuiltFromNothing(t *testing.T) { // T25
	t.Setenv("DOOMSDAY_SECRET_TOKEN", "leak")
	t.Setenv("MONOAGENT_VAULT", "leak")
	r := newRig(t)
	o := r.run("command", "env", "exit:0")
	if o.Result != ResPass {
		t.Fatalf("%+v", o)
	}
	for _, bad := range []string{"leak", "DOOMSDAY", "MONOAGENT", os.Getenv("HOME") + "/"} {
		if strings.Contains(o.Stdout, bad) {
			t.Fatalf("the environment carries %q:\n%s", bad, o.Stdout)
		}
	}
	if !strings.Contains(o.Stdout, "HOME="+r.sb.Scratch) || !strings.Contains(o.Stdout, "PATH="+SystemPath) {
		t.Fatalf("HOME is not in the scratch area:\n%s", o.Stdout)
	}
}

func TestNothingRunsWithoutPlacementAndASubjectRelativeProgramIsRefused(t *testing.T) {
	r := newRig(t)
	r.sb.Place = nil
	if o := r.run("command", "echo hi", "hi"); o.Result != ResError || !strings.Contains(o.Detail, "containment") {
		t.Fatalf("%+v", o)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	r.sb.Place = func(*exec.Cmd) (func(), error) { return nil, errors.New("the unit is gone") }
	if o := r.run("command", "touch "+marker, ""); o.Result != ResError || !strings.Contains(o.Detail, "not started") {
		t.Fatalf("%+v", o)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the program ran although it could not be placed")
	}
	r.sb.Place = func(*exec.Cmd) (func(), error) { return func() {}, nil }
	for _, spec := range []string{"./tool.sh", "tool.sh", "../x"} {
		if o := r.run("command", spec, ""); o.Result != ResError {
			t.Fatalf("%q: %+v", spec, o)
		}
	}
}

func TestSnapshotChangedUnderTheHarnessIsTainted(t *testing.T) {
	r := newRig(t)
	os.Chmod(r.snap, 0o700)
	os.Chmod(filepath.Join(r.snap, "a.txt"), 0o600)
	os.WriteFile(filepath.Join(r.snap, "a.txt"), []byte("changed"), 0o600)
	if o := r.run("command", "true", ""); o.Result != ResTainted {
		t.Fatalf("%+v", o)
	}
}

func TestFileCriteria(t *testing.T) { // T14
	r := newRig(t)
	sum := sha256.Sum256([]byte("alpha"))
	for _, c := range []struct{ spec, exp, want string }{
		{"a.txt", "exists", ResPass}, {"nope", "exists", ResFail}, {"nope", "absent", ResPass}, {"a.txt", "absent", ResFail},
		{"a.txt", "sha256:" + hex.EncodeToString(sum[:]), ResPass}, {"a.txt", "sha256:" + strings.Repeat("0", 64), ResFail},
		{"a.txt", "size<=5", ResPass}, {"a.txt", "size<=4", ResFail}, {"a.txt/x", "exists", ResFail},
	} {
		if o := r.run("file", c.spec, c.exp); o.Result != c.want {
			t.Fatalf("%v: %+v", c, o)
		}
	}
	// a link is never followed
	root := t.TempDir()
	write(t, root, "real", "x", 0o644)
	os.Symlink("real", filepath.Join(root, "ln"))
	snap := filepath.Join(t.TempDir(), "s")
	tree, _ := Snapshot(root, snap)
	defer RemoveTree(snap)
	p, _ := ParsePlan("file", "ln", "sha256:"+hex.EncodeToString(sum[:]))
	if o := r.sb.Run(context.Background(), snap, tree, p); o.Result != ResFail {
		t.Fatalf("a link was followed: %+v", o)
	}
}
