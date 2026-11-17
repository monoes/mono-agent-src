package main

import (
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/librarycli"
	"github.com/monoes/mono-agent/internal/cli/opgate"
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
		DBPath:            func() string { return cfg.DBPath },
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
		Refusal:         opgate.Refusal,
	}
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
