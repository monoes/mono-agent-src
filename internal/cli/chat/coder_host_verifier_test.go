package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The goal checks (MONOAGENT_PURSUIT_VERIFIER) are OFF by default and build nothing then; they need the judge flag; a checker that
// cannot be set up leaves the loop as it is and says why. verifierBuilder is replaced so that no real unit is ever started here.
func stubVerifierBuilder(t *testing.T, deps *coderhost.VerifierDeps, info coderhost.VerifierInfo) *int {
	t.Helper()
	calls := new(int)
	old := verifierBuilder
	verifierBuilder = func(context.Context, string, string, coderhost.ForbiddenFolders) (*coderhost.VerifierDeps, coderhost.VerifierInfo) {
		*calls++
		return deps, info
	}
	t.Cleanup(func() { verifierBuilder = old })
	return calls
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestTheGoalChecksAreOffByDefaultAndNothingIsBuilt(t *testing.T) {
	calls := stubVerifierBuilder(t, &coderhost.VerifierDeps{Pin: "x/y"}, coderhost.VerifierInfo{})
	d := &coderhost.PursuitDeps{}
	info := coderhost.PursuitHostInfo{Enabled: true, Pin: "claude/haiku"}
	for _, v := range []string{"", "   "} {
		attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: v}), d, &info, "/s", coderhost.ForbiddenFolders{})
	}
	if *calls != 0 || d.Verifier != nil || info.VerifierPin != "" || info.VerifierOn || info.VerifierReason != "" || info.Confinement != "" {
		t.Fatalf("with the flag unset: built %d times, deps %+v, info %+v", *calls, d.Verifier, info)
	}
	if line := pursuitStatusLine(info); strings.Contains(line, "goal checks") {
		t.Errorf("an off flag prints a goal-check line: %q", line)
	}
}

func TestTheGoalChecksNeedTheJudgeFlag(t *testing.T) {
	calls := stubVerifierBuilder(t, &coderhost.VerifierDeps{Pin: "x/y"}, coderhost.VerifierInfo{})
	info := coderhost.PursuitHostInfo{}
	attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: "gemini/checker"}), nil, &info, "/s", coderhost.ForbiddenFolders{})
	if *calls != 0 || info.VerifierOn || info.VerifierPin != "gemini/checker" || !strings.Contains(info.VerifierReason, envPursuitJudge) {
		t.Fatalf("calls %d, info %+v", *calls, info)
	}
	if line := pursuitStatusLine(info); !strings.Contains(line, "goal checks OFF") || !strings.Contains(line, envPursuitJudge) {
		t.Errorf("status line %q", line)
	}
}

func TestAGoalCheckerThatCannotBeSetUpLeavesTheLoopAsItIs(t *testing.T) {
	stubVerifierBuilder(t, nil, coderhost.VerifierInfo{Reason: "the pin is not runtime/model", Confinement: ""})
	d := &coderhost.PursuitDeps{}
	info := coderhost.PursuitHostInfo{Enabled: true, Pin: "claude/haiku"}
	attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: "bad pin"}), d, &info, "/s", coderhost.ForbiddenFolders{})
	if d.Verifier != nil || info.VerifierOn || !info.Enabled || info.VerifierReason == "" {
		t.Fatalf("deps %+v info %+v", d.Verifier, info)
	}
}

func TestAGoalCheckerThatIsSetUpIsWiredIntoTheDepsAndShown(t *testing.T) {
	want := &coderhost.VerifierDeps{Pin: "gemini/checker"}
	stubVerifierBuilder(t, want, coderhost.VerifierInfo{Enabled: true, Confinement: "systemd-run (x)"})
	d := &coderhost.PursuitDeps{}
	info := coderhost.PursuitHostInfo{Enabled: true, Pin: "claude/haiku", WakeCapUSD: 2}
	attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: " gemini/checker "}), d, &info, "/s", coderhost.ForbiddenFolders{})
	if d.Verifier != want || !info.VerifierOn || info.VerifierPin != "gemini/checker" || info.Confinement != "systemd-run (x)" {
		t.Fatalf("deps %+v info %+v", d.Verifier, info)
	}
	if line := pursuitStatusLine(info); !strings.Contains(line, "goal checks ON") || !strings.Contains(line, "$0.50") {
		t.Errorf("status line %q", line)
	}
}

// The production host builds no verifier when the judge is on and the verifier flag is not.
func TestTheProductionHostBuildsNoVerifierWithoutItsFlag(t *testing.T) {
	calls := stubVerifierBuilder(t, &coderhost.VerifierDeps{Pin: "x/y"}, coderhost.VerifierInfo{Enabled: true})
	built, _ := runProductionHost(t, map[string]string{envPursuitJudge: "claude/haiku@4"})
	if built.Pursuit == nil || built.Pursuit.Verifier != nil || *calls != 0 {
		t.Fatalf("pursuit %+v, verifier builder called %d times", built.Pursuit, *calls)
	}
}

func TestTheProductionHostWiresTheVerifierOnlyWithTheFlagAndTheJudge(t *testing.T) {
	want := &coderhost.VerifierDeps{Pin: "gemini/checker"}
	calls := stubVerifierBuilder(t, want, coderhost.VerifierInfo{Enabled: true, Confinement: "stub"})
	built, _ := runProductionHost(t, map[string]string{envPursuitJudge: "claude/haiku@4", envPursuitVerifier: "gemini/checker"})
	if built.Pursuit == nil || built.Pursuit.Verifier != want || *calls != 1 {
		t.Fatalf("pursuit %+v, builder calls %d", built.Pursuit, *calls)
	}
}

// The real forbidden list, built the way the production host builds it: projects under the home folder are fine; the home folder
// itself, what contains it, the state folder and what is inside it are not; links are resolved first.
func TestTheRealForbiddenListLetsProjectsUnderHomeThrough(t *testing.T) {
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".monoagent")
	for _, d := range []string{state, filepath.Join(state, "x"), filepath.Join(home, "projects", "x")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := testDeps(&testConfig{DBPath: filepath.Join(state, "monoagent.db"), ProfileID: "default"})
	f := verifierForbidden(cfg)
	link := filepath.Join(t.TempDir(), "link")
	linkState := filepath.Join(t.TempDir(), "linkstate")
	linkProj := filepath.Join(t.TempDir(), "linkproj")
	for from, to := range map[string]string{link: home, linkState: state, linkProj: filepath.Join(home, "projects", "x")} {
		if err := os.Symlink(to, from); err != nil {
			t.Skip("no symbolic links here")
		}
	}
	for name, c := range map[string]struct {
		dir string
		ok  bool
	}{
		"a project under home":           {filepath.Join(home, "projects", "x"), true},
		"home itself":                    {home, false},
		"the parent of home":             {filepath.Dir(home), false},
		"the root of the computer":       {string(filepath.Separator), false},
		"the state folder":               {state, false},
		"inside the state folder":        {filepath.Join(state, "x"), false},
		"a link to home":                 {link, false},
		"a link to the state folder":     {linkState, false},
		"a link to a project under home": {linkProj, true},
	} {
		real, err := filepath.EvalSymlinks(c.dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if why := coderhost.SubjectForbidden(real, f); (why == "") != c.ok {
			t.Errorf("%s: %q refused=%q, want ok=%v", name, real, why, c.ok)
		}
	}
}
