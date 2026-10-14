package main

import (
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/librarycli"
	"github.com/monoes/mono-agent/internal/storage"
)

// `library` lives in internal/cli/librarycli; this is its adapter: the CLI's globals and
// helpers handed over as librarycli.Deps, and the helpers `account` shares with it under
// their old names.

func newLibraryCmd(cfg *globalConfig) *cobra.Command { return librarycli.NewCommand(libraryDeps(cfg)) }

func libraryDeps(cfg *globalConfig) librarycli.Deps {
	return librarycli.Deps{
		JSONOutput:        func() bool { return cfg.JSONOutput },
		ProfileID:         func() string { return cfg.ProfileID },
		OpenDB:            func() (*storage.Database, error) { return initDB(cfg) },
		OpenRegistry:      openAutomationRegistry,
		OpenWorkflowStore: openWorkflowStore,
		WithJSONErrors:    func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) },
		AddSignIn: func(cmd *cobra.Command, name string) func(*cobra.Command) (account.Status, bool, error) {
			return addSignIn(cfg, cmd, name)
		},
		NewWorkflowImportJSONCmd: func() *cobra.Command {
			sub := *cfg
			sub.JSONOutput = true
			return newWorkflowImportCmd(&sub)
		},
		Bundle:          workflowBundleDeps(),
		LoginRequired:   libraryLoginRequired,
		IsLoginRequired: isLoginRequired,
	}
}

// printLib writes v as indented JSON with --json, else calls human.
func printLib(cfg *globalConfig, cmd *cobra.Command, v any, human func(w io.Writer)) error {
	if cfg.JSONOutput {
		return writeJSONTo(cmd.OutOrStdout(), v)
	}
	human(cmd.OutOrStdout())
	return nil
}

// openURLInBrowser opens url in the system's default browser; a var, so tests can stub it out.
var openURLInBrowser = clicore.OpenURLInBrowser

// openLoginURL opens the authorize URL in the default browser. Windows'
// `start` would split the URL at its '&'s, so it goes through
// url.dll's protocol handler there.
var openLoginURL = func(u string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return openURLInBrowser(u)
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimSpace(b.String()), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if err == io.EOF && b.Len() > 0 {
				return strings.TrimSpace(b.String()), nil
			}
			return "", err
		}
	}
}
