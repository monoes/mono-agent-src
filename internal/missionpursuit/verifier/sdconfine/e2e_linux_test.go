//go:build linux

package sdconfine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"github.com/monoes/mono-agent/internal/monomind"
)

type freeMeter struct{}

func (freeMeter) Reserve(context.Context, float64) (func(int64, bool), error) {
	return func(int64, bool) {}, nil
}

func hash64(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }

// The whole check on a real manager: the checker proves the confiner (its light canaries), hides the live project, runs a command
// and an output criterion in a unit on the sandbox's copy, and says it was confined. The reviewer is a fake that approves.
func TestRealACheckRunsItsCommandsInAUnitOnACopyAndSaysSo(t *testing.T) {
	r := newRealRig(t)
	root := filepath.Join(filepath.Dir(r.base), "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the self test of this check runs the light canaries only (the heavy ones are the real tests' business)
	r.c.canaries = func(ctx context.Context, conf verifier.Confiner, base string, o verifier.CanaryOptions) []verifier.CanaryResult {
		o.SkipHeavy = true
		o.HomeDir, o.RuntimeDir, o.TmpDir = r.home, r.rt, r.tmpdir
		return verifier.RunCanariesWith(ctx, conf, base, o)
	}
	k, err := verifier.New(verifier.Config{Checker: "gemini/checker-1@2026", ScratchBase: r.base, Meter: freeMeter{}, Confiner: r.c,
		Workspace: func() (string, error) { return "/ws", nil },
		Exec: func(_ context.Context, _ monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
			on(monomind.Event{Type: monomind.EventStart, Runtime: "gemini", Model: "checker-1"})
			return &monomind.TurnResult{SawDone: true, ResultText: `{"verdict":"ok","reason":"fine"}`}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	crit := func(id, kind, spec, exp string) verifier.Criterion {
		return verifier.Criterion{ID: id, Hash: hash64(id + spec), Kind: kind, Spec: spec, Expected: exp}
	}
	res, err := k.Run(context.Background(), verifier.Request{Root: root, GoalHash: hash64("goal"), Round: 1,
		Criteria: []verifier.Criterion{crit("c1", "command", "test -f a.txt", "exit:0"), crit("c2", "output", `sh -c 'echo "$HOME" | grep -q scratch && echo hi'`, "hi"),
			crit("c3", "command", "false", "exit:0")},
		JudgePin: "codex/gpt-judge", LeadPin: "claude/lead-model", LeadPinNow: func() string { return "claude/lead-model" }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Containment != "confined: systemd-run + process-group" {
		t.Fatalf("containment %q", res.Containment)
	}
	got := map[string]string{}
	for _, c := range res.Criteria {
		got[c.ID] = c.Result
	}
	if got["c1"] != "pass" || got["c2"] != "pass" || got["c3"] != "fail" || res.Outcome != verifier.OutcomeFailed {
		t.Fatalf("%+v\n%+v", got, res)
	}
	if strings.Contains(res.Message(), "UNCONFINED") {
		t.Fatalf("%q", res.Message())
	}
	noUnitsLeft(t, r.c)
}
