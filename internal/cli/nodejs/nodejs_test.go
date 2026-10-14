package nodejs

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent: nothing here downloads or removes a Node.js.
func TestMain(m *testing.M) { testhome.Main(m) }

func run(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(Deps{JSONOutput: func() bool { return jsonOut }, StdinIsTerminal: func() bool { return false }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestGroupHasEverySubcommand(t *testing.T) {
	t.Parallel()
	var got []string
	for _, c := range NewCommand(Deps{}).Commands() {
		got = append(got, c.Name())
	}
	if strings.Join(got, " ") != "install remove status update" {
		t.Fatalf("subcommands = %v", got)
	}
}

// With no managed Node in the test home, status says which Node is in use; update and a
// remove without --yes refuse.
func TestStatusUpdateRemoveWithoutManagedNode(t *testing.T) {
	var st struct {
		MinVersion string   `json:"min_version"`
		Installed  []string `json:"installed"`
		Active     string   `json:"active"`
		Managed    string   `json:"managed_version"`
	}
	out, err := run(t, true, "status")
	if err != nil || json.Unmarshal([]byte(out), &st) != nil || st.MinVersion == "" || st.Installed == nil || st.Managed != "" ||
		(st.Active != "system" && st.Active != "none") {
		t.Fatalf("status --json: %v %s", err, out)
	}
	if out, err := run(t, false, "status"); err != nil || !strings.Contains(out, "managed Node: not installed") {
		t.Fatalf("status: %v %s", err, out)
	}
	if _, err := run(t, false, "update"); clicore.ExitCodeFor(err) != 2 {
		t.Fatalf("update without a managed Node: %v", err)
	}
	if _, err := run(t, false, "remove"); clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "without --yes") {
		t.Fatalf("remove without --yes: %v", err)
	}
	if _, err := run(t, true, "remove", "24.0.0"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("remove --json without --yes: %v", err)
	}
}

func TestFinishStreamed(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := FinishStreamed(&b, false, nil, "done it"); err != nil || b.String() != "✓ done it\n" {
		t.Fatalf("text ok: %v %q", err, b.String())
	}
	b.Reset()
	if err := FinishStreamed(&b, true, nil, "done it"); err != nil || !strings.Contains(b.String(), `"kind":"done"`) {
		t.Fatalf("json ok: %v %q", err, b.String())
	}
	b.Reset()
	err := FinishStreamed(&b, true, clicore.NotFound("gone"), "x")
	if clicore.ExitCodeFor(err) != 2 || !strings.Contains(b.String(), `"kind":"error"`) {
		t.Fatalf("json error: %v %q", err, b.String())
	}
	boom := errors.New("boom")
	if err := FinishStreamed(&b, false, boom, "x"); err != boom {
		t.Fatalf("text error: %v", err)
	}
}
