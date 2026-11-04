package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

type matrixPkg struct{ action.PackageContext }

func (matrixPkg) Trust() string            { return "recorded" }
func (matrixPkg) ID() string               { return "acme" }
func (matrixPkg) StartURL() string         { return "" }
func (matrixPkg) Domains() []string        { return nil }
func (matrixPkg) PermittedSteps() []string { return nil }

type matrixSource struct{}

func (matrixSource) Load(string, string) ([]byte, error) { return nil, fmt.Errorf("not found") }
func (matrixSource) List() ([]string, error)             { return nil, nil }
func (matrixSource) Package(a string) action.PackageContext {
	if a == "acme" {
		return matrixPkg{}
	}
	return nil
}

// `node run` hands every node opgate.Refusal() as its verdict and the browser run gate reads the marker's text
// first: for every pair of marker variables with every MONOAGENT_ACTOR value, the verdict is the text
// operator.Refusal gave, and the gate's refusal with the verdict is byte-identical to the one without it.
// Needs the combined-marker fix of the turn-aware gate (#1405); until it is merged this fails on the pairs that
// carry MONOAGENT_AI_CALLER or MONOAGENT_WORKFLOW_RUN.
func TestNodeRunVerdictIsTheMarkedRefusalWhateverCombinationOfMarkersIsCarried(t *testing.T) {
	cfg := sendEnv(t)
	opgate.SetForTest(t, cfg.gateRefusal)
	action.SetDefSource(matrixSource{})
	t.Cleanup(func() { action.SetDefSource(nil) })
	b := nodes.NewBrowserNode("acme", "press")
	names := operator.MarkerEnv()
	cases := 0
	for i, a := range names {
		for _, c := range names[i:] {
			for _, actor := range []string{"", "claude", "operator"} {
				asOperatorProcess(t)
				t.Setenv(a, "x")
				t.Setenv(c, "x")
				t.Setenv("MONOAGENT_ACTOR", actor)
				want := operator.Refusal()
				if want == "" {
					continue
				}
				cases++
				got := opgate.Refusal()
				if got != want {
					t.Errorf("%s + %s + ACTOR=%q: opgate.Refusal() = %q, operator.Refusal() = %q", a, c, actor, got, want)
					continue
				}
				plain := b.RunRefusal(context.Background())
				carried := b.RunRefusal(workflow.WithAgentCaller(context.Background(), got))
				if plain == nil || carried == nil || plain.Error() != carried.Error() {
					t.Errorf("%s + %s + ACTOR=%q: run gate %v, with the verdict %v", a, c, actor, plain, carried)
				}
			}
		}
	}
	if cases < 20 {
		t.Fatalf("only %d marker combinations were exercised: the matrix is stale", cases)
	}
}
