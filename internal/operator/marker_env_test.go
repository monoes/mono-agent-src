package operator

import (
	"slices"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
)

// MarkerEnv covers Marker: with every variable in it cleared a process is the
// operator's, and each one that marks a process on its own is in it. (The
// agent kind and its token only refine an agent; alone they mark nothing.)
func TestMarkerEnvCoversMarker(t *testing.T) {
	env := MarkerEnv()
	for _, k := range env {
		t.Setenv(k, "x")
	}
	if Marker() == "" {
		t.Fatal("every marker set, yet the operator's terminal")
	}
	for _, k := range env {
		t.Setenv(k, "")
	}
	if m := Marker(); m != "" {
		t.Fatalf("MarkerEnv cleared, Marker still says %q", m)
	}
	for _, k := range env {
		// The kind and the run token mark nothing alone: the token is scrubbed from children with the markers
		// because it is a credential, and it always comes with the workflow mark.
		if k == browserctl.EnvAgentKind || k == browserctl.EnvAgentKindToken || k == workflow.EnvRunToken {
			continue
		}
		t.Setenv(k, "x")
		if Marker() == "" {
			t.Errorf("%s alone does not mark the process: drop it from MarkerEnv or say why it stays", k)
		}
		t.Setenv(k, "")
	}
	if !slices.Contains(env, "MONOAGENT_WORKFLOW_RUN") || !slices.Contains(env, "MONOAGENT_AI_CALLER") {
		t.Errorf("MarkerEnv lacks the run markers: %v", env)
	}
}
