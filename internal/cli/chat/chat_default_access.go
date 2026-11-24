package chat

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// withDefaultAccess makes `chat history create` without --access/--mode take
// coder mode's default access (`coder set --default-access`). It only fills in
// the flags the command already has, so the creation itself, including its
// operator check on every level above scoped, is the one that always ran.
func withDefaultAccess(cfg *Deps, cmd *cobra.Command) *cobra.Command {
	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(c, args); err != nil {
				return err
			}
		}
		f := c.Flags()
		if f.Changed("access") || f.Changed("mode") {
			return nil
		}
		level := defaultCreateAccess(c, cfg)
		if level == ai.AccessScoped {
			return nil
		}
		if err := f.Set("access", level); err != nil {
			return err
		}
		// Say it: a bare create from a terminal is not scoped (stderr, so a JSON result on stdout stays parseable).
		fmt.Fprintf(c.ErrOrStderr(), "note: no --access given; creating this conversation at the coder default access %q (pass --access to choose)\n", level)
		// The app's default folder, unless the caller named one.
		if !(f.Changed("cwd") || f.Changed("profile-folder") || f.Changed("coder-root") || f.Changed("new-workspace")) {
			return f.Set("profile-folder", "true")
		}
		return nil
	}
	return cmd
}

// defaultCreateAccess is the level a creation with no --access/--mode makes:
// the configured default (full when none is set) when the caller is the operator, coder mode is on and
// the requested runtime is ready for it, else scoped. An agent, a workflow or
// the chat's runs always get scoped: the default never lifts one of them
// above what it could create by naming the level.
func defaultCreateAccess(cmd *cobra.Command, cfg *Deps) string {
	if clicore.RequireOperator(cfg.RequireOperator, "create a conversation at the default access") != nil {
		return ai.AccessScoped
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return ai.AccessScoped
	}
	defer db.Close()
	s, err := loadCoderSettings(db.DB)
	if err != nil || !s.Enabled || s.defaultAccess() == ai.AccessScoped {
		return ai.AccessScoped
	}
	runtimeID, _ := cmd.Flags().GetString("runtime")
	if _, _, _, err := requireCoderReady(cmd, db.DB, runtimeID); err != nil {
		return ai.AccessScoped
	}
	return s.defaultAccess()
}
