package orgdecide

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// A runtime streams its reply as assistant deltas and the result event has
// no text; monomind.Exec's ResultText then holds only the last delta. The
// decider must parse the whole reply (found in a live run).
func TestModelDeciderParsesStreamedDeltas(t *testing.T) {
	chunks := []string{"```json\n{\"verdict\": \"", "approve", "\", \"rationale", "\": \"policy allows it", "\"}\n```"}
	d := &ModelDecider{Runtime: "claude", Model: "m", Exec: func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		res := &monomind.TurnResult{}
		for _, c := range chunks {
			ev := monomind.Event{Type: monomind.EventAssistant, Text: c}
			onEvent(ev)
			monomind.ApplyEventToResult(res, ev)
		}
		res.CostUSD = 0.01
		return res, nil
	}}
	out, err := d.Decide(context.Background(), Prompt{Allowed: []string{"approve", "deny"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict.Verdict != "approve" || out.Verdict.Rationale != "policy allows it" || out.CostUSD != 0.01 {
		t.Fatalf("outcome = %+v", out)
	}
}

// The decider runs in an empty folder of its own under ~/.monoagent, not
// the daemon's working directory, and the same one every time so agent CLIs
// do not collect session state per decision (C-46 live gate).
func TestModelDeciderRunsInAnEmptyFolder(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	// Something a previous decision left behind is cleared first.
	leftover := filepath.Join(home, ".monoagent", "decider", "leftover")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	d := &ModelDecider{Runtime: "claude", Model: "m", Exec: func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		dirs = append(dirs, opts.Cwd)
		if opts.Sandbox != monomind.TurnSandboxMode {
			t.Errorf("decider turn Sandbox = %q, want %q", opts.Sandbox, monomind.TurnSandboxMode)
		}
		entries, err := os.ReadDir(opts.Cwd)
		if err != nil || len(entries) != 0 {
			t.Errorf("decider folder %q: %d entries, err %v", opts.Cwd, len(entries), err)
		}
		onEvent(monomind.Event{Type: monomind.EventAssistant, Text: `{"verdict": "deny", "rationale": "no"}`})
		return &monomind.TurnResult{}, nil
	}}
	for i := 0; i < 2; i++ {
		if _, err := d.Decide(context.Background(), Prompt{Allowed: []string{"approve", "deny"}}); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(home, ".monoagent", "decider")
	if len(dirs) != 2 || dirs[0] != want || dirs[1] != want {
		t.Fatalf("decider ran in %v, want %s both times", dirs, want)
	}
}
