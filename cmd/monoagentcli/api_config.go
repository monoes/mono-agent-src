package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/autostart"
	apiconfigcli "github.com/monoes/mono-agent/internal/cli/apiconfig"
	"github.com/monoes/mono-agent/internal/openaiapi"
	"github.com/monoes/mono-agent/internal/storage"
)

// `api config show|set|unset` and `api models` live in internal/cli/apiconfig; this is their adapter: the
// CLI's globals and helpers handed over as apiconfigcli.Deps.

// newInstaller is the OS service manager that `api config` asks whether the daemon can be
// restarted and `daemon restart` asks to restart it. Tests replace it: nothing of this package
// may run launchctl, systemctl or schtasks in a test.
var newInstaller = autostart.New

func configEnv() apiconfig.Env { return apiconfig.Env{Installer: newInstaller()} }

// newAPIConfigCmd is the group that shows, changes and removes the settings of the
// OpenAI-compatible API's server saved in the database.
func newAPIConfigCmd(cfg *globalConfig) *cobra.Command {
	return apiconfigcli.NewCommand(apiConfigDeps(cfg))
}

// newAPIModelsCmd is `api models` (internal/cli/apiconfig).
func newAPIModelsCmd(cfg *globalConfig) *cobra.Command {
	return apiConfigDeps(cfg).NewModelsCommand()
}

func apiConfigDeps(cfg *globalConfig) apiconfigcli.Deps {
	return apiconfigcli.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		Env:          configEnv,
		InvalidInput: errInvalidInput,
		AsCLIError:   asCLIError,
		WriteJSON:    writeJSONTo,
		ProfileID:    func() string { return cfg.ProfileID },
		SavedEnv:     savedEnv,
	}
}

// The document `api models --json` prints is the one the MCP tool api_models_list returns.
type (
	apiAutoJSON   = openaiapi.AutoReport
	apiModelsJSON = openaiapi.ModelsReport
)

// autoNote is the line the text output gives for the auto model (apiconfigcli.AutoNote).
func autoNote(a apiAutoJSON) string { return apiconfigcli.AutoNote(a) }

// asCLIError gives the errors of internal/apiconfig the exit codes of the CLI
// (apiconfigcli.AsCLIError).
func asCLIError(err error) error { return apiconfigcli.AsCLIError(err) }
