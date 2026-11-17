package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/crashreport"
	"github.com/monoes/mono-agent/internal/cli/dbcli"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/cli/releasecli"
	"github.com/monoes/mono-agent/internal/cli/statuscli"
	"github.com/monoes/mono-agent/internal/storage"
)

// `status`, `db migrate` and `release` live in internal/cli/statuscli, dbcli and releasecli, and
// the crash report in internal/cli/crashreport; these are their adapters: the CLI's globals
// handed over as each package's Deps.

func newStatusCmd(cfg *globalConfig) *cobra.Command {
	return statuscli.NewCommand(statuscli.Deps{
		JSONOutput:    func() bool { return cfg.JSONOutput },
		OpenDB:        func() (*storage.Database, error) { return initDB(cfg) },
		DBPath:        func() string { return cfg.DBPath },
		ProfileID:     func() string { return cfg.ProfileID },
		StatusDaemons: statusDaemons,
	})
}

// `db migrate` brings the database and every profile's folder up to date; the desktop app runs
// `monoagentcli --json db migrate` once at startup.
func newDBCmd(cfg *globalConfig) *cobra.Command {
	return dbcli.NewCommand(dbcli.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
		DBPath:     func() string { return cfg.DBPath },
	})
}

// dbMigrateResult is `db migrate --json`.
type dbMigrateResult = dbcli.Result

func newReleaseCmd() *cobra.Command {
	return releasecli.NewCommand(releasecli.Deps{Refusal: opgate.Refusal})
}

// reportCrash is called from a deferred recover() in main(): crashreport.Report for this build.
func reportCrash(panicValue interface{}, stack []byte) {
	crashreport.Report(panicValue, stack, crashreport.Build{Version: getVersion(), BuildDate: getBuildDate(), Repo: releasesRepo})
}
