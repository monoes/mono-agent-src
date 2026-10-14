package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/data"
	doctorcli "github.com/monoes/mono-agent/internal/cli/doctor"
	"github.com/monoes/mono-agent/internal/health"
)

// Skills move from flat <name>.md files to <name>/SKILL.md folders. The old
// flat file goes only when it is exactly what a release wrote; an edited
// one stays.
func TestClaudeSkillsMigrateFromFlatFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	same, edited := claudeSkillNames[0], claudeSkillNames[1]
	want, err := data.SkillsFS.ReadFile("skills/" + same + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, same+".md"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, edited+".md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	runClaudeFirstRunCheck()
	for _, name := range claudeSkillNames {
		if _, err := os.Stat(claudeSkillFile(skills, name)); err != nil {
			t.Errorf("%s/SKILL.md not installed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(skills, same+".md")); !os.IsNotExist(err) {
		t.Errorf("identical flat file should be removed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(skills, edited+".md")); string(b) != "mine" {
		t.Errorf("edited flat file must stay, got %q", b)
	}
}

// runDoctor runs `monoagentcli <args>` against a throwaway HOME.
func runDoctor(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append(args, "--db-path", filepath.Join(home, ".monoagent", "monoagent.db")))
	err := root.Execute()
	return out.String(), err
}

func decodeDoctor(t *testing.T, out string) doctorJSON {
	t.Helper()
	var rep struct {
		health.Report
		Fixes []fixOutcome `json:"fixes"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json output is not JSON: %v\n%s", err, out)
	}
	return doctorJSON{Report: &rep.Report, Fixes: rep.Fixes}
}

func resultByID(rep doctorJSON, id string) health.Result {
	for _, r := range rep.Results {
		if r.ID == id {
			return r
		}
	}
	return health.Result{}
}

func TestDoctorFreshHomeFailsThenFixes(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()

	out, err := runDoctor(t, home, "doctor", "--json", "--group", "core")
	if exitCodeFor(err) != 1 {
		t.Fatalf("fresh home: exit %d (%v), want 1", exitCodeFor(err), err)
	}
	rep := decodeDoctor(t, out)
	if rep.V != health.SchemaVersion {
		t.Fatalf("schema v%d", rep.V)
	}
	// A truly fresh home: doctor itself creates nothing, so the data folder
	// is missing and the checks that need it wait.
	home0 := resultByID(rep, health.CheckHome)
	if home0.Status != health.StatusFail || home0.Fix == nil {
		t.Fatalf("core.home on fresh home: %+v", home0)
	}
	for _, id := range []string{health.CheckDB, health.CheckProfile} {
		if st := resultByID(rep, id).Status; st != health.StatusSkip {
			t.Fatalf("%s should wait on the data folder, got %q", id, st)
		}
	}

	out, err = runDoctor(t, home, "doctor", "--json", "--group", "core", "--fix")
	if err != nil {
		t.Fatalf("doctor --fix: %v\n%s", err, out)
	}
	rep = decodeDoctor(t, out)
	for _, id := range []string{health.CheckHome, health.CheckDB, health.CheckProfile} {
		if st := resultByID(rep, id).Status; st != health.StatusOK {
			t.Errorf("%s after --fix: %q", id, st)
		}
	}
	applied := map[string]string{}
	for _, f := range rep.Fixes {
		applied[f.ID] = f.Outcome
	}
	if applied[home0.Fix.ID] != "applied" || applied[health.FixDBMigrate] != "applied" || applied[health.FixProfileLayout] != "applied" {
		t.Errorf("fix outcomes: %v", rep.Fixes)
	}
}

func TestDoctorFixStreamsNDJSON(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()

	out, err := runDoctor(t, home, "doctor", "fix", health.FixDBMigrate, "--json")
	if err != nil {
		t.Fatalf("doctor fix: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		var ev fixEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("non-NDJSON line %q", l)
		}
	}
	var last fixEvent
	_ = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Kind != "done" {
		t.Fatalf("last event %+v, want done", last)
	}

	if _, err := runDoctor(t, home, "doctor", "fix", "no.such.fix"); exitCodeFor(err) != 2 {
		t.Fatalf("unknown fix: exit %d, want 2", exitCodeFor(err))
	}
}

// A plain doctor run changes nothing: not the data folder, not Claude's
// skills folder (the root command's first-run setup is not run for it), so
// the data-folder check can see the folder missing.
func TestDoctorChangesNothing(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _ := runDoctor(t, home, "doctor", "--json", "--group", "core")
	for _, p := range []string{filepath.Join(home, ".monoagent"), filepath.Join(home, ".claude", "skills")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("doctor created %s", p)
		}
	}
	if st := resultByID(decodeDoctor(t, out), health.CheckHome).Status; st == health.StatusOK {
		t.Errorf("data-folder check passed with no ~/.monoagent: %q", st)
	}
}

// --profile takes a name as well as an id, as for every other command.
func TestDoctorProfileByName(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	if out, err := runDoctor(t, home, "doctor", "--json", "--group", "core", "--fix"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if out, err := runDoctor(t, home, "profile", "create", "Work"); err != nil {
		t.Fatalf("profile create: %v\n%s", err, out)
	}
	out, _ := runDoctor(t, home, "doctor", "--json", "--group", "core", "--profile", "Work")
	// Resolved to its id: the check names the profile, and may only warn
	// that a new profile's folder is not made yet.
	if r := resultByID(decodeDoctor(t, out), health.CheckProfile); r.Status == health.StatusFail || !strings.HasPrefix(r.Summary, "Work (") {
		t.Fatalf("--profile Work: %+v, want the profile found by name", r)
	}
}

// doctor puts a managed Node on PATH before its checks, as every other
// command does: the monomind checks start monomind, whose shebang needs
// `node`, and on a machine whose only Node is the managed one they would
// otherwise report monomind broken and offer a fix that never takes.
func TestDoctorActivatesTheManagedNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script node")
	}
	keyring.MockInit()
	home := t.TempDir()
	bin := filepath.Join(home, ".monoagent", "node", "24.1.0", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\necho v24.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".monoagent", "node", "current"), []byte("24.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no system node
	_, _ = runDoctor(t, home, "doctor", "--json", "--group", "core")
	if first := filepath.SplitList(os.Getenv("PATH"))[0]; first != bin {
		t.Fatalf("PATH after doctor starts with %q, want the managed Node's bin %q", first, bin)
	}
}

// The check that runs before every command installs skills that are
// missing but never rewrites one that is there: an edit the user made, or
// a copy another monoagent binary wrote, survives every command.
func TestFirstRunCheckKeepsExistingSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	skills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	edited := claudeSkillFile(skills, claudeSkillNames[0])
	if err := os.MkdirAll(filepath.Dir(edited), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited, []byte("my own notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	runClaudeFirstRunCheck()
	if b, _ := os.ReadFile(edited); string(b) != "my own notes" {
		t.Fatalf("an existing skill was rewritten: %q", b)
	}
	for _, name := range claudeSkillNames[1:] {
		if _, err := os.Stat(claudeSkillFile(skills, name)); err != nil {
			t.Errorf("missing skill %s was not installed: %v", name, err)
		}
	}
}

// Without a managed Node, packages a system npm put in the private prefix
// (its own global prefix wasn't writable) are still found: doctor appends
// ~/.monoagent/npm-global/bin to PATH, after everything already there.
func TestDoctorActivatesNpmGlobalWithoutManagedNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix layout")
	}
	keyring.MockInit()
	home := t.TempDir()
	npmBin := filepath.Join(home, ".monoagent", "npm-global", "bin")
	if err := os.MkdirAll(npmBin, 0o755); err != nil {
		t.Fatal(err)
	}
	sys := t.TempDir()
	t.Setenv("PATH", sys)
	_, _ = runDoctor(t, home, "doctor", "--json", "--group", "core")
	parts := filepath.SplitList(os.Getenv("PATH"))
	if parts[0] != sys || parts[len(parts)-1] != npmBin {
		t.Fatalf("PATH after doctor = %v, want %s first and %s appended", parts, sys, npmBin)
	}
}

// The documents `doctor --json` and `setup --json` print (internal/cli/doctor).
type (
	doctorJSON = doctorcli.Report
	setupJSON  = doctorcli.SetupReport
	fixOutcome = doctorcli.FixOutcome
)
