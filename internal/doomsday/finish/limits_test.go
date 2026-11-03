//go:build !windows

package finish

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	r := newRig(t)
	r.sb.Limits = Limits{CPUSeconds: 7, OpenFiles: 33, FileSize: 4096 * 512, Procs: 3001, AddressSpace: 5 << 30}
	o := r.run("command", "sh -c 'ulimit -t; ulimit -n; ulimit -u; ulimit -v'", "")
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
	os.Exit(m.Run())
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
