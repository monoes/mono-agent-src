package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/capturehooks"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestTheCommandHasItsFlagsAndSubcommands(t *testing.T) {
	c := NewCommand(Deps{})
	for _, f := range []string{"api", "api-addr", "allow-mutations", "bridge", "v1-addr", "confinement", "context-confinement", "auto-confinement", "max-concurrent"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("daemon has no --%s", f)
		}
	}
	var subs []string
	for _, s := range c.Commands() {
		subs = append(subs, s.Name())
	}
	if got := strings.Join(subs, " "); got != "install restart uninstall" {
		t.Errorf("subcommands = %q", got)
	}
}

// A word that is no subcommand is refused, never taken as `daemon` itself:
// `daemon status` used to start a foreground daemon instead of saying there
// is no such command. Mounted under a root, as in the real CLI, because cobra
// already refuses stray words on a root command.
func TestTheDaemonRefusesAnUnknownSubcommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	built := false
	root := &cobra.Command{Use: "monoagentcli"}
	root.AddCommand(NewCommand(Deps{BuildEngine: func() (*workflow.WorkflowEngine, func(), error) {
		built = true
		return nil, func() {}, errors.New("must not build")
	}}))
	root.SetArgs([]string{"daemon", "status", "--bridge=false"})
	root.SilenceErrors, root.SilenceUsage = true, true
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown command "status"`) {
		t.Fatalf("Execute = %v, want unknown command \"status\"", err)
	}
	if got := clicore.ExitCodeFor(err); got != 3 {
		t.Errorf("exit code = %d, want 3", got)
	}
	if got := clicore.JSONErrorCode(err); got != "invalid_input" {
		t.Errorf("JSON error code = %q, want invalid_input", got)
	}
	if built {
		t.Fatal("the daemon started for an unknown subcommand")
	}
}

// The daemon takes the home's lock before anything starts, and an engine that
// cannot be built ends the command with the reason.
func TestTheDaemonStopsWhenTheEngineCannotBeBuilt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	boom := errors.New("boom")
	c := NewCommand(Deps{BuildEngine: func() (*workflow.WorkflowEngine, func(), error) { return nil, func() {}, boom }})
	c.SetArgs([]string{"--bridge=false"})
	c.SilenceErrors, c.SilenceUsage = true, true
	if err := c.Execute(); !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "build engine: ") {
		t.Fatalf("Execute = %v, want build engine: boom", err)
	}
}

// A bridge that is already running is relayed through, not raced for: the
// daemon builds no server of its own.
func TestTheDaemonBridgeYieldsToARunningOne(t *testing.T) {
	running := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(extension.Status{Service: extension.ServiceName, Addr: "127.0.0.1:4242"})
	}))
	defer running.Close()
	addr, closeFn, err := startDaemonBridge(context.Background(), Deps{
		ProbeAddrs: func() []string { return []string{running.URL} },
		NewExtensionServer: func(zerolog.Logger) *extension.Server {
			t.Fatal("a server was built while a bridge runs")
			return nil
		},
		InstallCaptureHooks: func(*extension.Server, func(string, ...any)) *capturehooks.Hooks {
			t.Fatal("capture hooks installed while a bridge runs")
			return nil
		},
	})
	if err != nil || addr != "127.0.0.1:4242" {
		t.Fatalf("startDaemonBridge = %q, %v; want the running bridge's address", addr, err)
	}
	closeFn()
}
