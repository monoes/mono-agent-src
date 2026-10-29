package extensioncli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/operator"
)

// runExt runs `extension <args>` with d.
func runExt(t *testing.T, d *Deps, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(*d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func operatorOnlyErr(err error) bool {
	var fields interface{ JSONErrorFields() map[string]any }
	return clicore.ExitCodeFor(err) == 3 && errors.As(err, &fields) && fields.JSONErrorFields()["code"] == "operator_only"
}

// gateHome is a test HOME with a pairing token and an installed extension,
// and no bridge; zip is an extension zip to install.
func gateHome(t *testing.T) (home, zip string) {
	t.Helper()
	pairTestHome(t)
	home, _ = os.UserHomeDir()
	if _, err := extension.CurrentToken(); err != nil {
		t.Fatal(err)
	}
	if err := installExtensionZip(fixtureExtensionZip(t, map[string]string{"chrome-extension/manifest.json": "v1"}),
		filepath.Join(home, ".monoagent", "extension")); err != nil {
		t.Fatal(err)
	}
	zip = filepath.Join(t.TempDir(), "ext.zip")
	if err := os.WriteFile(zip, fixtureExtensionZip(t, map[string]string{"chrome-extension/manifest.json": "v2"}), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, zip
}

// gated are the commands that change which browser or profile the
// extension acts as, which bridge it pairs with, or the code it runs.
func gated(zip string) [][]string {
	return [][]string{{"bind", "Edge", "Work"}, {"unbind", "Edge"}, {"unbind", "Edge", "Work"}, {"assign", "Work", "Edge"}, {"assign", "Work"}, {"browsers", "allow", "abcd1234", "--any"}, {"reset"}, {"pair", "--open"}, {"install", "--zip", zip}}
}

// A marked process — an agent's shell, the chat, a workflow's run, a coder
// turn, an org role — is refused each of them (exit 3, operator_only), and
// nothing changes: the token and the installed extension stay as they were.
func TestExtensionOperatorGatesRefuseEveryMarker(t *testing.T) {
	home, zip := gateHome(t)
	tokenPath := filepath.Join(home, ".monoagent", "extension.token")
	manifest := filepath.Join(home, ".monoagent", "extension", "manifest.json")
	markers := map[string]string{"CLAUDECODE": "1", "MONOAGENT_AI_CALLER": "chat", "MONOAGENT_WORKFLOW_RUN": "exec-1",
		"MONOAGENT_AGENT_CONTEXT": "1", "MONOMIND_ORG_ROLE": "writer", "CODEX_SANDBOX": "seatbelt"}
	for _, k := range operator.MarkerEnv() {
		if _, ok := markers[k]; !ok {
			markers[k] = "1"
		}
	}
	tested := 0
	for k, v := range markers {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, v)
			if operator.Refusal() == "" {
				t.Skipf("%s marks a process only with another marker", k)
			}
			tested++
			for _, args := range gated(zip) {
				out, err := runExt(t, testDeps(false), args...)
				if !operatorOnlyErr(err) || !strings.Contains(err.Error(), k) {
					t.Errorf("%v under %s: %v\n%s", args, k, err, out)
				}
			}
			if _, err := os.Stat(tokenPath); err != nil {
				t.Errorf("the token is gone: %v", err)
			}
			if b, _ := os.ReadFile(manifest); string(b) != "v1" {
				t.Errorf("the extension was replaced: %q", b)
			}
			// Printing the token stays open (it goes to the process's stdout).
			if _, err := runExt(t, testDeps(false), "pair"); err != nil {
				t.Errorf("pair under %s: %v", k, err)
			}
		})
	}
	if tested < 6 {
		t.Fatalf("only %d markers refused", tested)
	}
}

// The operator gets through: reset revokes the token, install --zip
// replaces the extension, and bind and unbind go on to the bridge (none
// runs here). Without an operator check wired in, each refuses.
func TestExtensionOperatorGatesLetTheOperatorThrough(t *testing.T) {
	home, zip := gateHome(t)
	dbPath := filepath.Join(t.TempDir(), "gates.db")
	withProfile := testDepsDB(false, dbPath)
	db, err := withProfile.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, root_dir) VALUES ('work', 'Work', ?)`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, args := range [][]string{{"bind", "Edge", "Work"}, {"unbind", "Edge"}} {
		if _, err := runExt(t, withProfile, args...); clicore.ExitCodeFor(err) != 2 || !strings.Contains(err.Error(), "No bridge is running") {
			t.Errorf("%v as the operator: %v", args, err)
		}
	}
	if _, err := runExt(t, testDeps(false), "install", "--zip", zip); err != nil {
		t.Fatalf("install --zip: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".monoagent", "extension", "manifest.json")); string(b) != "v2" {
		t.Errorf("install --zip as the operator: manifest %q", b)
	}
	if _, err := runExt(t, testDeps(false), "reset"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "extension.token")); !os.IsNotExist(err) {
		t.Errorf("reset as the operator left the token: %v", err)
	}

	for _, args := range gated(zip) {
		d := testDeps(false)
		d.Refusal = nil
		if _, err := runExt(t, d, args...); !operatorOnlyErr(err) || !strings.Contains(err.Error(), "operator check is not wired") {
			t.Errorf("%v with no operator check: %v", args, err)
		}
	}
}
