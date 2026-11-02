package browserctl

import (
	"os"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// actAsHuman clears every agent marker for the test.
func actAsHuman(t *testing.T) {
	t.Helper()
	for _, k := range AgentContextEnv() {
		t.Setenv(k, "")
	}
	for _, k := range []string{"MONOAGENT_CONVERSATION_ID", "MONOAGENT_AGENT_ROLE", "MONOAGENT_AGENT_ID", "MONOMIND_ORG_NAME", EnvCoderTitle} {
		t.Setenv(k, "")
	}
}

func TestAgentContextDetection(t *testing.T) {
	actAsHuman(t)
	if IsAgentContext() {
		t.Fatal("no markers set, still an agent")
	}
	if got := ContextKey(); got != "human" {
		t.Fatalf("ContextKey = %q, want human", got)
	}
	for _, marker := range []string{"CLAUDECODE", "MONOMIND_AGENT_EXEC", "CODEX_SANDBOX", "MONOAGENT_AGENT_CONTEXT"} {
		t.Run(marker, func(t *testing.T) {
			t.Setenv(marker, "1")
			if !IsAgentContext() {
				t.Fatalf("%s set, not an agent", marker)
			}
			if got := ContextKey(); got != "agent" {
				t.Fatalf("ContextKey = %q, want agent", got)
			}
		})
	}
}

func TestContextKeyAndGroupTitle(t *testing.T) {
	actAsHuman(t)
	t.Setenv("MONOMIND_ORG_ROLE", "qa lead")
	t.Setenv("MONOMIND_ORG_NAME", "acme/web")
	if got := ContextKey(); got != "role-acme_web-qa_lead" {
		t.Fatalf("ContextKey = %q", got)
	}
	if got := GroupTitle(); got != "Coder · qa lead" {
		t.Fatalf("GroupTitle = %q", got)
	}
	t.Setenv("MONOAGENT_CONVERSATION_ID", "c-42/../x")
	if got := ContextKey(); got != "conv-c-42_.._x" {
		t.Fatalf("ContextKey = %q", got)
	}
	t.Setenv(EnvCoderTitle, "Fix the login page")
	if got := GroupTitle(); got != "Coder · Fix the login page" {
		t.Fatalf("GroupTitle = %q", got)
	}
	t.Setenv("MONOMIND_ORG_ROLE", "")
	t.Setenv(EnvCoderTitle, "")
	t.Setenv("CLAUDECODE", "1")
	if got := GroupTitle(); got != "Coder · Agent" {
		t.Fatalf("GroupTitle = %q", got)
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if st := LoadState("human"); st.TabID != 0 {
		t.Fatalf("fresh state = %+v", st)
	}
	if err := SaveState("conv-1", State{TabID: 7, Instance: "i", Reported: map[string]int{"i/7": 3}}); err != nil {
		t.Fatal(err)
	}
	st := LoadState("conv-1")
	if st.TabID != 7 || st.Instance != "i" || st.Reported["i/7"] != 3 || st.UpdatedAt == "" {
		t.Fatalf("state = %+v", st)
	}
	dir, _ := StateDir()
	fi, err := os.Stat(dir + "/conv-1.json")
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) { // Windows has no Unix modes
		t.Fatalf("state file: %v, %v", fi, err)
	}
}
