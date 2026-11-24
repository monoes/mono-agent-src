package chat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// createAs runs `chat history create <args>` as the operator or as an agent
// (whose operator check refuses, as the real one does inside an agent's shell).
func createAs(t *testing.T, dbPath string, operator bool, args ...string) (string, int) {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	if !operator {
		cfg.RequireOperator = func(what string) error { return clicore.OperatorOnly("only the user can %s", what) }
	}
	var err error
	out := captureStdout(t, func() {
		cmd := newChatCmd(cfg)
		cmd.SetArgs(append([]string{"history", "create", "--runtime", "claude"}, args...))
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return out, exitCodeFor(err)
}

func accessOf(t *testing.T, out string) string {
	t.Helper()
	var c ai.ConversationRecord
	decodeChatJSON(t, out, &c)
	return c.Access
}

func TestDefaultAccessAppliesOnlyToTheOperator(t *testing.T) {
	r := newAccessRig(t, true)
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, DefaultAccess: ai.AccessWorkspace})

	out, code := createAs(t, r.dbPath, true, "--new-workspace")
	if code != 0 || accessOf(t, out) != ai.AccessWorkspace {
		t.Fatalf("operator creation (exit %d) = %s, want the default workspace", code, out)
	}
	// An agent that names no level stays scoped: the default is not a way up.
	out, code = createAs(t, r.dbPath, false)
	if code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("agent creation (exit %d) = %s, want scoped", code, out)
	}
	// Against the merged creation check: naming a level above scoped is still
	// the operator's, and the default does not open a way round it.
	if _, code = createAs(t, r.dbPath, false, "--access", "workspace", "--new-workspace"); code != 3 {
		t.Fatalf("agent creation above scoped: exit %d, want 3", code)
	}
	// An explicit level beats the default.
	out, code = createAs(t, r.dbPath, true, "--access", "scoped")
	if code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("explicit scoped (exit %d) = %s", code, out)
	}
}

func TestDefaultAccessFallsBackToScoped(t *testing.T) {
	r := newAccessRig(t, true)
	// Coder mode off: the default is not used and creation does not fail.
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: false, DefaultAccess: ai.AccessFull})
	if out, code := createAs(t, r.dbPath, true); code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("coder off (exit %d) = %s", code, out)
	}
	// A runtime coder mode cannot drive (codex has no full access in the rig).
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, DefaultAccess: ai.AccessFull})
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default", JSONOutput: true})
	var err error
	out := captureStdout(t, func() {
		cmd := newChatCmd(cfg)
		cmd.SetArgs([]string{"history", "create", "--runtime", "codex"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	if err != nil || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("not-ready runtime (err %v) = %s", err, out)
	}
}

func TestCoderSetDefaultAccess(t *testing.T) {
	r := newAccessRig(t, true)
	if out, code := runCoderCLI(t, r.dbPath, "set", "--default-access", "coding"); code != 0 {
		t.Fatalf("set exit %d: %s", code, out)
	}
	out, _ := runCoderCLI(t, r.dbPath, "status")
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if st.DefaultAccess != ai.AccessCoding {
		t.Fatalf("defaultAccess = %q", st.DefaultAccess)
	}
	if _, code := runCoderCLI(t, r.dbPath, "set", "--default-access", "root"); code == 0 {
		t.Error("an unknown level must be refused")
	}
	if st.DefaultAccessSource != "set" {
		t.Fatalf("source = %q, want set", st.DefaultAccessSource)
	}
	// Unset reads as full, from the built-in default.
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true})
	out, _ = runCoderCLI(t, r.dbPath, "status")
	st = coderStatus{}
	decodeChatJSON(t, out, &st)
	if st.DefaultAccess != ai.AccessFull || st.DefaultAccessSource != "default" {
		t.Fatalf("default = %q from %q, want full from default", st.DefaultAccess, st.DefaultAccessSource)
	}
}

func TestCoderSetDefaultAccessIsOperatorOnly(t *testing.T) {
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(what string) error { return clicore.OperatorOnly("only the user can %s", what) }
	cmd := newCoderCmd(cfg)
	cmd.SetArgs([]string{"set", "--default-access", "full"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); exitCodeFor(err) != 3 {
		t.Fatalf("agent set exit %d (%v), want 3", exitCodeFor(err), err)
	}
}

// Nothing stored: an operator's new chat is a full-access coder chat when
// coder mode is on and ready; everything else is as before.
func TestUnsetDefaultAccessIsFullOnlyForTheOperatorWithCoderOn(t *testing.T) {
	r := newAccessRig(t, true)
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true})
	out, code := createAs(t, r.dbPath, true)
	if code != 0 || accessOf(t, out) != ai.AccessFull {
		t.Fatalf("operator, coder on, unset (exit %d) = %s, want full", code, out)
	}
	if out, code = createAs(t, r.dbPath, false); code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("agent, unset (exit %d) = %s, want scoped", code, out)
	}
	if out, code = createAs(t, r.dbPath, true, "--access", "coding", "--new-workspace"); code != 0 || accessOf(t, out) != ai.AccessCoding {
		t.Fatalf("explicit coding (exit %d) = %s", code, out)
	}
	if out, code = createAs(t, r.dbPath, true, "--mode", "assistant"); code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("explicit --mode assistant (exit %d) = %s", code, out)
	}
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, DefaultAccess: ai.AccessScoped})
	if out, code = createAs(t, r.dbPath, true); code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("stored scoped (exit %d) = %s, want scoped", code, out)
	}
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: false})
	if out, code = createAs(t, r.dbPath, true); code != 0 || accessOf(t, out) != ai.AccessScoped {
		t.Fatalf("coder off, unset (exit %d) = %s, want scoped", code, out)
	}
}

// R09-11 (printing part): a create that takes the default level says which level it took.
func TestDefaultAccessIsPrintedOnCreate(t *testing.T) {
	r := newAccessRig(t, true)
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, DefaultAccess: ai.AccessWorkspace})
	run := func(args ...string) string {
		cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default", JSONOutput: true})
		var stderr bytes.Buffer
		captureStdout(t, func() {
			cmd := newChatCmd(cfg)
			cmd.SetErr(&stderr)
			cmd.SetArgs(append([]string{"history", "create", "--runtime", "claude"}, args...))
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			_ = cmd.Execute()
		})
		return stderr.String()
	}
	if got := run("--new-workspace"); !strings.Contains(got, `"workspace"`) {
		t.Fatalf("a default-level create printed %q, want the level named", got)
	}
	if got := run("--access", "scoped"); got != "" {
		t.Fatalf("an explicit level printed %q", got)
	}
}
