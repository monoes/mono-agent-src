package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	actiondef "github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/autoinstall"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

type resultErr struct {
	err error
	res *automation.InstallResult
}

func (e *resultErr) Error() string { return e.err.Error() }
func (e *resultErr) Unwrap() error { return e.err }

// jsonErr stands in for the CLI's --json error document.
type jsonErr struct{ error }

func (e jsonErr) Unwrap() error { return e.error }

// testDeps is the CLI as the commands see it, on a registry of its own.
func testDeps(t *testing.T, jsonOut bool) (Deps, *automation.Registry) {
	t.Helper()
	reg, err := automation.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Deps{
		Install: autoinstall.Deps{
			JSONOutput:      func() bool { return jsonOut },
			OpenRegistry:    func() (*automation.Registry, error) { return reg, nil },
			StdinIsTerminal: func() bool { return false },
			WithInstallResult: func(err error, res *automation.InstallResult) error {
				if err == nil || res == nil {
					return err
				}
				return &resultErr{err, res}
			},
			InstallResultOf: func(err error) *automation.InstallResult {
				var re *resultErr
				if errors.As(err, &re) {
					return re.res
				}
				return nil
			},
			Reported: func(err error) error { return err },
			NotFound: func(format string, a ...any) error { return fmt.Errorf(format, a...) },
		},
		WithJSONErrors: func(cmd *cobra.Command) {
			run := cmd.RunE
			if run == nil {
				return
			}
			cmd.RunE = func(c *cobra.Command, args []string) error {
				if err := run(c, args); err != nil {
					return jsonErr{err}
				}
				return nil
			}
		},
	}, reg
}

func run(t *testing.T, d Deps, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewCommand(d)
	var out, errb bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// writeAction writes a loose ActionDef file and returns its path.
func writeAction(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "act.json")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

const looseAction = `{"actionType":"grab","automation":"acme","sideEffects":"none",
	"steps":[{"id":"n","type":"navigate","url":"https://app.acme.test/x"},{"id":"s","type":"log"}]}`

func TestGroupWrapsEverySubcommand(t *testing.T) {
	t.Parallel()
	d, _ := testDeps(t, false)
	_, _, err := run(t, d, "export", "no-dot")
	var je jsonErr
	if !errors.As(err, &je) || !strings.Contains(err.Error(), "expected <automation>.<action>") {
		t.Fatalf("export: %v", err)
	}
	_, _, err = run(t, d, "template", "list", "extra")
	if err == nil {
		t.Fatal("template list takes no args")
	}
}

func TestImportLooseActionThenExportAndList(t *testing.T) {
	t.Parallel()
	d, reg := testDeps(t, true)
	file := writeAction(t, looseAction)

	// Nobody can answer: an import needs --yes.
	if _, _, err := run(t, d, "import", file); !errors.Is(err, autoinstall.ErrConfirmationRequired) {
		t.Fatalf("unconfirmed import: %v", err)
	}
	out, _, err := run(t, d, "import", file, "--yes")
	var res automation.InstallResult
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.ID != "acme" || !res.Installed {
		t.Fatalf("import: %v %s", err, out)
	}
	info, err := reg.Info("acme")
	if err != nil || info.Source != automation.SourceLocal {
		t.Fatalf("a loose action file installs as local: %+v %v", info, err)
	}

	mpkg := filepath.Join(t.TempDir(), "grab.mpkg")
	out, _, err = run(t, d, "export", "acme.grab", "-o", mpkg)
	if err != nil || !strings.Contains(out, `"sha256"`) {
		t.Fatalf("export: %v %s", err, out)
	}
	p, err := automation.OpenFile(mpkg)
	if err != nil || strings.Join(p.Manifest.Actions, ",") != "grab" || strings.Join(p.Manifest.Site.Domains, ",") != "app.acme.test" {
		t.Fatalf("exported package: %+v %v", p, err)
	}

	out, _, err = run(t, d, "template", "list")
	if err != nil || !strings.Contains(out, `"node_type": "acme.grab"`) {
		t.Fatalf("template list: %v %s", err, out)
	}
}

func TestTemplateInstallAndEmptyList(t *testing.T) {
	t.Parallel()
	d, _ := testDeps(t, false)
	out, _, err := run(t, d, "template", "list")
	if err != nil || !strings.HasPrefix(out, "No local or recorded actions.") {
		t.Fatalf("empty list: %v %q", err, out)
	}
	out, errOut, err := run(t, d, "template", "install", writeAction(t, looseAction))
	if err != nil || !strings.Contains(errOut, "deprecated") || !strings.Contains(out, "Run it: monoagent node run acme.grab") {
		t.Fatalf("template install: %v %q %q", err, out, errOut)
	}
}

func TestWrapActionJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, wantErr, wantTier string
	}{
		{name: "site from navigate", body: looseAction, wantTier: "standard"},
		{name: "social platform", body: `{"actionType":"a","platform":"instagram","steps":[{"type":"navigate","url":"https://www.instagram.com/"}]}`, wantTier: "social"},
		{name: "no site", body: `{"actionType":"a","automation":"acme","steps":[{"type":"navigate","url":"{{url}}"}]}`, wantErr: "cannot tell which site"},
		{name: "unsafe name", body: `{"actionType":"../a","automation":"acme","steps":[{"type":"log"}]}`, wantErr: "invalid characters"},
		{name: "no steps", body: `{"actionType":"a","automation":"acme","steps":[]}`, wantErr: "has no steps"},
		{name: "bad json", body: `{`, wantErr: "invalid ActionDef JSON"},
	} {
		p, cleanup, err := wrapActionJSON(writeAction(t, tc.body), nil)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: err %v", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if p.Manifest.Policy.Tier != tc.wantTier || p.Manifest.Site.StartURL == "" {
			t.Fatalf("%s: manifest %+v", tc.name, p.Manifest)
		}
		cleanup()
	}
}

func TestNavigateHostsAndStepTypes(t *testing.T) {
	t.Parallel()
	steps := []actiondef.StepDef{
		{Type: "navigate", URL: "https://B.test/x"},
		{Type: "loop", Steps: []actiondef.StepDef{{Type: "navigate", URL: "http://a.test/"}, {Type: "navigate", URL: "https://{{x}}/"}}},
		{Type: "navigate", URL: "https://b.test/y"},
		{Type: "navigate", URL: "file:///etc"},
	}
	if got := strings.Join(navigateHosts(steps), ","); got != "b.test,a.test" {
		t.Fatalf("hosts %q", got)
	}
	if got := strings.Join(stepTypes(steps), ","); got != "loop,navigate" {
		t.Fatalf("step types %q", got)
	}
	if actionImportSource("x.JSON") != automation.SourceLocal || actionImportSource("x.mpkg") != automation.SourceImported {
		t.Fatal("import source")
	}
}
