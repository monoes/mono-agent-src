//go:build !windows

package finish

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testhome"
)

// writeProg writes an executable script into a directory that becomes the sandbox's PATH.
func writeProg(t *testing.T, r *rig, name, body string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, name, "#!/bin/sh\n"+body+"\n", 0o755)
	r.sb.Path = dir + ":/usr/bin:/bin"
}

// Each process limit is applied to the program itself, by the child, before it runs (note 13.6 section 5, T29).
func TestCriterionRunsUnderItsProcessLimits(t *testing.T) {
	// POSIX sh (dash on the CI runners) has no `ulimit -u`: read the limits through a shell that does.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this machine: the only way this test reads RLIMIT_NPROC (ulimit -u) is through it")
	}
	r := newRig(t)
	r.sb.Limits = Limits{CPUSeconds: 7, OpenFiles: 33, FileSize: 4096 * 512, Procs: 3001, AddressSpace: 5 << 30}
	o := r.run("command", bash+" -c 'ulimit -t; ulimit -n; ulimit -u; ulimit -v'", "")
	want := "7\n33\n3001\n5242880" // -v counts KiB
	if got := strings.TrimSpace(o.Stdout); got != want {
		t.Fatalf("limits seen by the program = %q, want %q (%+v)", got, want, o)
	}
}

// A file larger than the file-size limit cannot be written; the shell reports it and the criterion fails on the exit code.
func TestCriterionCannotWriteAFileOverTheFileSizeLimit(t *testing.T) {
	r := newRig(t)
	writeProg(t, r, "bigfile", `dd if=/dev/zero of="$TMPDIR/big" bs=1024 count=64 2>/dev/null; echo "exit=$?"; ls -l "$TMPDIR/big" | cut -d' ' -f5`)
	r.sb.Limits = Limits{FileSize: 8192}
	o := r.run("command", "bigfile", "")
	if !strings.Contains(o.Stdout, "exit=") || strings.Contains(o.Stdout, "exit=0") {
		t.Fatalf("a 64 KiB write passed an 8 KiB file limit: %+v", o)
	}
}

// The cap covers every writable tree: the criterion's cwd is the working copy of the subject, and RLIMIT_FSIZE bounds one file
// only. Many small files written there past the cap kill the program while it runs (the result is then tainted by the
// changed working copy, which parks the finish; what matters is that the program did not run to its end). Mutant tried:
// polling only the scratch directory (the old check) lets "finished" through.
func TestCriterionIsKilledWhenItsSubjectCopyPassesTheCap(t *testing.T) {
	r := newRig(t)
	writeProg(t, r, "filler", `chmod u+w .; i=0; while [ $i -lt 400 ]; do dd if=/dev/zero of="./f$i" bs=1024 count=4 2>/dev/null; i=$((i+1)); sleep 0.01; done; echo finished`)
	r.sb.Limits = Limits{ScratchBytes: 64 * 1024}
	o := r.run("command", "filler", "")
	if strings.Contains(o.Stdout, "finished") || o.Result == ResPass {
		t.Fatalf("a program wrote past the cap into the subject copy and ran to its end: %+v", o)
	}
	if o.Result != ResTainted && o.Result != ResError {
		t.Fatalf("%+v", o)
	}
	// the subject's own bytes are not the program's growth: a program that writes nothing is not touched by a tiny cap
	r.sb.Limits = Limits{ScratchBytes: 1024}
	writeProg(t, r, "quiet", `echo ok`)
	if o := r.run("command", "quiet", "ok"); o.Result != ResPass {
		t.Fatalf("%+v", o)
	}
}

// The scratch cap: many small files, each under the file limit, are still stopped by the poll while the program runs.
func TestCriterionIsKilledWhenItsScratchPassesTheCap(t *testing.T) {
	r := newRig(t)
	writeProg(t, r, "filler", `i=0; while [ $i -lt 400 ]; do dd if=/dev/zero of="$TMPDIR/f$i" bs=1024 count=4 2>/dev/null; i=$((i+1)); sleep 0.01; done; echo finished`)
	r.sb.Limits = Limits{ScratchBytes: 64 * 1024}
	o := r.run("command", "filler", "")
	if o.Result != ResError || !strings.Contains(o.Detail, "scratch") || strings.Contains(o.Stdout, "finished") {
		t.Fatalf("the scratch cap did not stop the program: %+v", o)
	}
	// and a program inside the cap is not touched
	r.sb.Limits = Limits{ScratchBytes: 4 << 20}
	if o := r.run("command", "filler", "finished"); o.Result != ResPass {
		t.Fatalf("%+v", o)
	}
}

// The check after the run catches what the poll missed (a program that ends right after it wrote past the cap).
func TestScratchBytesIsBoundedAndCountsOnlyRegularFiles(t *testing.T) {
	d := t.TempDir()
	write(t, d, "a", strings.Repeat("x", 100), 0o644)
	if err := os.Mkdir(filepath.Join(d, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d, "sub"), "b", strings.Repeat("y", 50), 0o644)
	if err := os.Symlink("/dev/zero", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	if n, over := scratchBytes(d, 1000); n != 150 || over {
		t.Fatalf("%d %v", n, over)
	}
	if _, over := scratchBytes(d, 120); !over {
		t.Fatal("150 bytes are not over 120")
	}
}

// A limit the child cannot set stops the program: it does not run with fewer limits.
func TestAProgramDoesNotRunWhenAPrivilegedLimitCannotBeSet(t *testing.T) {
	r := newRig(t)
	// raising a hard limit above the parent's is refused for an unprivileged process: ask for more open files than the hard limit
	r.sb.Limits = Limits{OpenFiles: 1 << 40}
	o := r.run("command", "echo hello", "hello")
	if o.Result != ResError || !strings.Contains(o.Detail, "limits could not be set") || strings.Contains(o.Stdout, "hello") {
		t.Fatalf("%+v", o)
	}
}

func TestLimitsSpecRefusesBadInput(t *testing.T) {
	for _, s := range []string{"", "1,2,3", "1,2,3,4,x", "1,2,3,4,0", "1,2,3,4,-1"} {
		if _, err := parseLimitsSpec(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if l, err := parseLimitsSpec("1,2,3,4,5"); err != nil || l.Procs != 5 || l.CPUSeconds != 1 {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestMain(m *testing.M) {
	// The tests re-exec this binary as the limits helper, as the command's run function does in production.
	RunLimitsHelperIfRequested(os.Args[1:])
	testhome.Main(m)
}

// No file of this package may declare an init(): the helper mode is run from the command's run function, because nothing
// may do I/O before main reads the credential frame (cmd/monoagentcli init_audit_test.go scans the linked build).
func TestThePackageDeclaresNoInit(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "init" {
				t.Errorf("%s declares init()", f)
			}
		}
	}
}

// skipIfDirsStayReadable skips where mode 000 does not stop the listing (a privileged user).
func skipIfDirsStayReadable(t *testing.T, d string) {
	t.Helper()
	if _, err := os.ReadDir(d); err == nil {
		t.Skip("a directory with mode 000 can still be listed here (running as root)")
	}
}

// A directory that cannot be listed hides its bytes from the walk, so it counts as over the cap (fail closed): a
// criterion that writes into a subdirectory and then removes its permissions must not drop those bytes from every poll.
// Mutants tried: the scratch walk skipping an unreadable directory (red on the first check); the subject walk doing the
// same (red on the second); counting an unreadable directory that was already unreadable before the run (the baseline is
// subtracted: red on the third, which a program that wrote nothing would otherwise fail).
func TestAnUnreadableDirectoryCountsAsOverTheCap(t *testing.T) {
	scratch := t.TempDir()
	sub := filepath.Join(scratch, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, sub, "big", strings.Repeat("x", 4096), 0o644)
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o700) })
	skipIfDirsStayReadable(t, sub)
	if _, over := scratchBytes(scratch, 1<<30); !over {
		t.Fatal("4 KiB hidden behind an unreadable directory were not over a 1 GiB cap")
	}

	subject := t.TempDir()
	w := newWritable(t.TempDir(), subject)
	hidden := filepath.Join(subject, "d")
	if err := os.Mkdir(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, hidden, "big", strings.Repeat("y", 4096), 0o644)
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(hidden, 0o700) })
	if !w.over(1 << 30) {
		t.Fatal("a subject directory made unreadable by the program was not over the cap")
	}
	if newWritable(t.TempDir(), subject).over(1 << 30) {
		t.Fatal("a directory already unreadable before the program ran was counted against it")
	}
}

// The subject's own bytes are not the program's growth: a working copy larger than the cap is inside it until the program
// adds to it. Mutant tried: dropping the baseline subtraction (red on the first check).
func TestSubjectBaselineIsNotCountedAgainstTheCap(t *testing.T) {
	subject := t.TempDir()
	write(t, subject, "big", strings.Repeat("s", 100<<10), 0o644)
	w := newWritable(t.TempDir(), subject)
	if w.over(10 << 10) {
		t.Fatal("the subject's own 100 KiB were counted against a 10 KiB growth cap")
	}
	write(t, subject, "more", strings.Repeat("m", 20<<10), 0o644)
	if !w.over(10 << 10) {
		t.Fatal("20 KiB of growth was not over a 10 KiB cap")
	}
}

// The check after the run catches what the poll missed. With the poll stopped, a program that writes past the cap and ends
// is judged by the final check alone. Mutant tried: removing the final area.over check (red: the result is a pass).
func TestPostRunCheckCatchesWhatThePollMissed(t *testing.T) {
	old := scratchPoll
	scratchPoll = time.Hour
	t.Cleanup(func() { scratchPoll = old })
	r := newRig(t)
	writeProg(t, r, "burst", `dd if=/dev/zero of="$HOME/f" bs=1024 count=40 2>/dev/null; dd if=/dev/zero of="$HOME/g" bs=1024 count=40 2>/dev/null; echo done`)
	r.sb.Limits = Limits{ScratchBytes: 64 * 1024}
	o := r.run("command", "burst", "done")
	if o.Result != ResError || !strings.Contains(o.Detail, "ended beyond") {
		t.Fatalf("the final check did not catch the burst: %+v", o)
	}
	// inside the cap the same program passes
	r.sb.Limits = Limits{ScratchBytes: 1 << 20}
	if o := r.run("command", "burst", "done"); o.Result != ResPass {
		t.Fatalf("%+v", o)
	}
}

// A criterion that writes into a subdirectory and then removes its permissions is not under the cap after the run.
func TestProgramThatHidesItsBytesBehindChmodIsOverTheCap(t *testing.T) {
	old := scratchPoll
	scratchPoll = time.Hour
	t.Cleanup(func() { scratchPoll = old })
	r := newRig(t)
	sub := filepath.Join(r.sb.Scratch, "hide")
	t.Cleanup(func() { os.Chmod(sub, 0o700) })
	writeProg(t, r, "hider", `mkdir "$HOME/hide"; dd if=/dev/zero of="$HOME/hide/f" bs=1024 count=40 2>/dev/null; dd if=/dev/zero of="$HOME/hide/g" bs=1024 count=40 2>/dev/null; chmod 000 "$HOME/hide"; echo done`)
	r.sb.Limits = Limits{ScratchBytes: 64 * 1024}
	if _, err := os.ReadDir(r.sb.Scratch); err != nil {
		t.Fatal(err)
	}
	o := r.run("command", "hider", "done")
	if _, err := os.ReadDir(sub); err == nil {
		t.Skip("a directory with mode 000 can still be listed here (running as root)")
	}
	if o.Result != ResError || !strings.Contains(o.Detail, "beyond") {
		t.Fatalf("bytes hidden behind chmod 000 were not over the cap: %+v", o)
	}
}
