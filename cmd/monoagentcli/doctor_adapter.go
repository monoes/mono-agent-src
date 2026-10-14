package main

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentinstall"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	doctorcli "github.com/monoes/mono-agent/internal/cli/doctor"
	"github.com/monoes/mono-agent/internal/health"
)

// `doctor` and `setup`, and the real machine they check (NewHealthEnv), live in
// internal/cli/doctor; this is their adapter: the CLI's helpers handed over as doctorcli.Deps
// and doctorcli.EnvDeps, and the old names of the helpers the rest of the CLI uses.

func doctorDeps(cfg *globalConfig) doctorcli.Deps {
	return doctorcli.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		NewEnv: func(projects []string) (*health.Env, func()) {
			return doctorcli.NewHealthEnv(healthEnvDeps(cfg), projects)
		},
		FinishStreamed: finishStreamed,
	}
}

// healthEnvDeps is the real machine's environment on cfg (--db-path, --profile).
func healthEnvDeps(cfg *globalConfig) doctorcli.EnvDeps {
	return doctorcli.EnvDeps{
		DBPath:    func() string { return cfg.DBPath },
		ProfileID: func() string { return cfg.ProfileID },
		Version:   getVersion,
		LatestVersion: func(ctx context.Context) (string, error) {
			info, err := fetchLatest(ctx, false)
			if err != nil {
				return "", err
			}
			return info.Tag, nil
		},
		MigrateDB: func() error {
			db, err := initDB(&globalConfig{DBPath: cfg.DBPath, ProfileID: cfg.ProfileID})
			if err != nil {
				return err
			}
			return db.Close()
		},
		InstallRuntime: func(ctx context.Context, id string, progress func(string)) error {
			_, err := installRuntime(ctx, realRuntimeMachine(), id, false, func(agentinstall.Recipe) bool { return true }, progress)
			return err
		},
		Hook: func(env *health.Env) {
			if healthEnvHook != nil {
				healthEnvHook(env)
			}
		},
	}
}

// healthEnvHook, when set (tests only), adjusts every environment doctor
// builds — e.g. replaces the hooks that start processes or use the network.
var healthEnvHook func(*health.Env)

// connectionSecrets and scrubSecrets keep a connection's secrets out of errors
// (doctorcli.ConnectionSecrets, doctorcli.ScrubSecrets); `connect` uses them.
var (
	connectionSecrets = doctorcli.ConnectionSecrets
	scrubSecrets      = doctorcli.ScrubSecrets
)

func newDoctorCmd(cfg *globalConfig) *cobra.Command {
	return doctorcli.NewDoctorCommand(doctorDeps(cfg))
}

func newSetupCmd(cfg *globalConfig) *cobra.Command { return doctorcli.NewSetupCommand(doctorDeps(cfg)) }

// fixEvent is one NDJSON progress event of a streamed fix.
type fixEvent = doctorcli.FixEvent

func writeFixEvent(w io.Writer, ev fixEvent) { doctorcli.WriteFixEvent(w, ev) }

// progressWriter streams fix output: NDJSON line events in JSON mode, indented text otherwise.
func progressWriter(w io.Writer, asJSON bool) func(string) {
	return doctorcli.ProgressWriter(w, asJSON)
}

// stdinIsTerminal reports whether a person can answer on stdin.
func stdinIsTerminal() bool { return clicore.StdinIsTerminal() }
