package chat

import (
	"context"
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
	verifierBuilder = func(context.Context, string, string, []string) (*coderhost.VerifierDeps, coderhost.VerifierInfo) {
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
		attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: v}), d, &info, "/s", nil)
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
	attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: "gemini/checker"}), nil, &info, "/s", nil)
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
	attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: "bad pin"}), d, &info, "/s", nil)
	if d.Verifier != nil || info.VerifierOn || !info.Enabled || info.VerifierReason == "" {
		t.Fatalf("deps %+v info %+v", d.Verifier, info)
	}
}

func TestAGoalCheckerThatIsSetUpIsWiredIntoTheDepsAndShown(t *testing.T) {
	want := &coderhost.VerifierDeps{Pin: "gemini/checker"}
	stubVerifierBuilder(t, want, coderhost.VerifierInfo{Enabled: true, Confinement: "systemd-run (x)"})
	d := &coderhost.PursuitDeps{}
	info := coderhost.PursuitHostInfo{Enabled: true, Pin: "claude/haiku", WakeCapUSD: 2}
	attachVerifier(context.Background(), envOf(map[string]string{envPursuitVerifier: " gemini/checker "}), d, &info, "/s", nil)
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
