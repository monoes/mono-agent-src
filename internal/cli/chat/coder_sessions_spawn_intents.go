package chat

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions spawn-intents`: the operator's end of a spawn the host's sweep held ('needs-operator': the child's
// task session was approved, or the cleanup kept failing). Both verbs are the operator's: the gate runs first, before
// any argument is read and before the database is opened.

const resolveIntentWords = "resolve spawn intent"

func newCoderSessionsSpawnIntentsCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spawn-intents",
		Short: "List the spawns the host holds for you, or end one (the operator's)",
		Long: "A `session_spawn` that was interrupted leaves an intent row; the host's sweep ends it, except when the child's task " +
			"session was approved meanwhile or the cleanup kept failing: then the intent is held for you ('needs-operator') and " +
			"keeps the lead from spawning. `list` shows every intent with the generation `resolve` needs.",
	}
	cmd.AddCommand(newSpawnIntentsListCmd(cfg), newSpawnIntentsResolveCmd(cfg))
	return cmd
}

func newSpawnIntentsListCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Every spawn intent of the profile: state, attempts, claimed_at, child, task sessions, generation",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMissionOperator(cfg, "list the spawn intents"); err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			views, err := coderhost.NewStore(db.DB).ListSpawnIntents(cfg.ProfileID())
			if err != nil {
				return err
			}
			if views == nil {
				views = []coderhost.SpawnIntentView{}
			}
			return printQueueJSON(cmd, map[string]any{"intents": views})
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newSpawnIntentsResolveCmd(cfg *Deps) *cobra.Command {
	var keep, discard bool
	var gen int64
	var confirm string
	cmd := &cobra.Command{
		Use:   "resolve SPAWNER --generation N (--keep | --discard) --confirm \"" + resolveIntentWords + "\"",
		Short: "End a held spawn intent: keep the spawn as accepted, or discard its draft child (the operator's)",
		Long: "Only a needs-operator row, only at the generation `list` showed. --keep writes one acceptance row for the spawn and " +
			"drops the intent; the child conversation and its task session stay. --discard removes the child and its draft task " +
			"session the way the sweep does, and refuses when the task session is approved or running. Either way you delete " +
			"the child conversation and the task session yourself if you want them gone. --confirm must be exactly \"" + resolveIntentWords + "\".",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMissionOperator(cfg, "resolve a held spawn intent"); err != nil {
				return err
			}
			if len(args) != 1 || keep == discard || gen <= 0 {
				return clicore.InvalidInput("give the SPAWNER, --generation N from `list`, and exactly one of --keep or --discard")
			}
			if confirm != resolveIntentWords {
				return clicore.InvalidInput("add --confirm %q to resolve a held spawn intent", resolveIntentWords)
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			err = coderhost.NewStore(db.DB).ResolveSpawnIntent(cmd.Context(), cfg.ProfileID(), args[0], gen, keep)
			if errors.Is(err, coderhost.ErrSpawnIntentUnknown) || errors.Is(err, coderhost.ErrSpawnIntentNotHeld) ||
				errors.Is(err, coderhost.ErrSpawnIntentStale) || errors.Is(err, coderhost.ErrSpawnIntentLive) {
				return clicore.InvalidInput("%v", err)
			}
			if err != nil {
				return err
			}
			note := "the child conversation and its task session were left as they are: the operator deletes those separately"
			if discard {
				note = "the draft child and its task session were removed"
			}
			return printQueueJSON(cmd, map[string]any{"spawner": args[0], "kept": keep, "note": note})
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "accept the spawn as it stands (one acceptance row; the child stays)")
	cmd.Flags().BoolVar(&discard, "discard", false, "remove the draft child and its task session (refused for an approved or running one)")
	cmd.Flags().Int64Var(&gen, "generation", 0, "the generation `list` showed for the intent")
	cmd.Flags().StringVar(&confirm, "confirm", "", "the words \""+resolveIntentWords+"\"")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
