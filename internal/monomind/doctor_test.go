package monomind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeMonomind writes a shell script that records its args and cwd and
// prints body, exiting with code.
func fakeMonomind(t *testing.T, body string, code int) (bin, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "monomind")
	script := "#!/bin/sh\necho \"$(pwd) $*\" > " + argsFile + "\necho 'human text' >&2\ncat <<'JSON'\n" + body + "\nJSON\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

const sampleReport = `{"v":1,"success":false,"summary":{"passed":1,"warnings":1,"failed":1,"info":0},
"results":[{"component":"node","name":"Node.js Version","status":"pass","message":"v24","fix":null,"fix_safety":null,"fix_flag":null},
{"component":"helpers","name":"Helper Files","status":"warn","message":"3 stale","fix":"monomind init upgrade","fix_safety":"auto","fix_flag":"--fix"}],
"fixes":[{"component":"helpers","outcome":"applied"}]}`

func TestDoctorParsesReportEvenOnExit1(t *testing.T) {
	bin, argsFile := fakeMonomind(t, sampleReport, 1)
	dir := t.TempDir()
	rep, err := Doctor(context.Background(), bin, DoctorOptions{Dir: dir, Component: "helpers", Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.V != 1 || len(rep.Results) != 2 || *rep.Results[1].FixSafety != "auto" || rep.Fixes[0].Outcome != "applied" {
		t.Fatalf("report: %+v", rep)
	}
	got, _ := os.ReadFile(argsFile)
	realDir, _ := filepath.EvalSymlinks(dir)
	if want := realDir + " doctor --json -c helpers --fix"; strings.TrimSpace(string(got)) != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
}

func TestDoctorRejectsNonReport(t *testing.T) {
	bin, _ := fakeMonomind(t, "Usage: monomind doctor", 0)
	if _, err := Doctor(context.Background(), bin, DoctorOptions{Dir: t.TempDir()}); err == nil {
		t.Fatal("want an error for non-JSON output")
	}
}

func TestProjectsUnder(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel, ".monomind")
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "config.yaml"), []byte("x"), 0o644)
	}
	mk("")                                                       // the root itself: not listed
	mk("codes/app")                                              // listed
	mk("codes/app/sub/deep")                                     // depth 4: listed? (codes=1, app=2, sub=3, deep=4) → no
	mk("node_modules/pkg")                                       // skipped dir
	os.MkdirAll(filepath.Join(root, "docs", ".monomind"), 0o755) // bare .monomind: not initialized
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	got := ProjectsUnder(root)
	want := []string{filepath.Join(root, "codes", "app")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectsUnder = %v, want %v", got, want)
	}
}

func TestJSONBodySkipsLeadingNotice(t *testing.T) {
	out := []byte("  ↑ monomind v2.16.2 available  →  run: npm install -g monomind@latest\n{\"v\":1}\n")
	if got := string(JSONBody(out)); got != "{\"v\":1}\n" {
		t.Fatalf("JSONBody = %q", got)
	}
	if got := string(JSONBody([]byte(`{"v":1}`))); got != `{"v":1}` {
		t.Fatalf("plain JSON changed: %q", got)
	}
	if got := string(JSONBody([]byte("no json here"))); got != "no json here" {
		t.Fatalf("non-JSON must come back unchanged: %q", got)
	}
}

// A report in a format this monoagent doesn't know is not read as "0
// checks, all passed": renamed fields would parse as empty.
func TestDoctorRejectsAnotherFormatVersion(t *testing.T) {
	bin, _ := fakeMonomind(t, `{"v":2,"outcome":{"ok":3}}`, 0)
	_, err := Doctor(context.Background(), bin, DoctorOptions{Dir: t.TempDir()})
	if !errors.Is(err, ErrDoctorFormat) {
		t.Fatalf("v2 report: err %v, want ErrDoctorFormat", err)
	}
}

// When the deadline ends monomind, a child still holding its output pipe
// does not keep Doctor waiting.
func TestDoctorReturnsWhenAChildHoldsThePipe(t *testing.T) {
	bin, _ := fakeMonomind(t, "", 0)
	script := "#!/bin/sh\nsleep 60 &\nsleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Doctor(ctx, bin, DoctorOptions{Dir: t.TempDir()}); err == nil {
		t.Fatal("want an error")
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("Doctor took %v after its deadline", d)
	}
}
