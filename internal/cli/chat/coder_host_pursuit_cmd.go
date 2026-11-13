package chat

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder host pursuit`: the operator's host-wide switch for the mission pursuit loop. The loop is ON by default and idles
// until a judge pin exists; these verbs save the pin or an explicit OFF in the reserved rows (coderhost/pursuit_switch.go).
// They are operator-only (refused inside a coder session or a mission task) and change nothing in a running host: the host
// reads them once, when it starts. The app shells out to these verbs and renders their output.
const restartNote = "restart the coder host to apply"

func newCoderHostPursuitCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pursuit",
		Short: "The host-wide switch of the mission pursuit loop (on by default, idle until a judge is chosen)",
		Long: "The pursuit loop is on by default but does nothing, and spends nothing, until a judge model is chosen " +
			"(`pursuit judge set RUNTIME/MODEL`, a model that differs from your leads'). A pursuit still starts only when you accept a " +
			"mission's goal and confirm it. The goal checker (MONOAGENT_PURSUIT_VERIFIER) is separate and stays off. " +
			"Changes apply when the coder host restarts.",
		Args: cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return coderHostPursuitStatus(cfg, cmd) }
	cmd.AddCommand(pursuitStatusCmd(cfg), pursuitOnOffCmd(cfg, true), pursuitOnOffCmd(cfg, false), pursuitJudgeCmd(cfg))
	return cmd
}

func pursuitStatusCmd(cfg *Deps) *cobra.Command {
	c := &cobra.Command{Use: "status", Short: "Show the saved switch, the judge and the first-run notice (read only)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return coderHostPursuitStatus(cfg, cmd) }}
	clicore.WithJSONErrors(cfg.JSONOutput, c)
	return c
}

func coderHostPursuitStatus(cfg *Deps, cmd *cobra.Command) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	st := coderhost.NewStore(db.DB)
	off, err := st.PursuitOperatorOff()
	if err != nil {
		return err
	}
	pin, err := st.SavedPursuitJudge()
	if err != nil {
		return err
	}
	host, found, alive := coderhost.ReadHostPursuitState(clicore.ExpandPath(cfg.DBPath()))
	out := map[string]any{"operatorOff": off, "savedJudge": pin, "notice": coderhost.PursuitNotice(), "pauseCommand": coderhost.PursuitPauseCommand,
		"host": map[string]any{"found": found, "alive": alive, "pursuit": host}}
	if cfg.JSONOutput() {
		return printQueueJSON(cmd, out)
	}
	w := cmd.OutOrStdout()
	state := "on by default"
	if off {
		state = "OFF (the operator turned it off)"
	}
	fmt.Fprintf(w, "saved switch: %s\nsaved judge: %q\n", state, pin)
	if found {
		fmt.Fprintln(w, "host: "+pursuitLoopLine(host))
	}
	fmt.Fprintln(w, coderhost.PursuitNotice())
	return nil
}

func pursuitOnOffCmd(cfg *Deps, on bool) *cobra.Command {
	use, short := "off", "Turn the pursuit loop off for this host (kept until you run `on`)"
	if on {
		use, short = "on", "Clear an explicit OFF: back to the default (on, idle until a judge is chosen)"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireMissionOperator(cfg, "turn the pursuit loop on or off"); err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			if err := coderhost.NewStore(db.DB).SetPursuitOperatorOff(!on); err != nil {
				return err
			}
			return printQueueJSON(cmd, map[string]any{"operatorOff": !on, "note": restartNote})
		}}
	clicore.WithJSONErrors(cfg.JSONOutput, c)
	return c
}

func pursuitJudgeCmd(cfg *Deps) *cobra.Command {
	set := &cobra.Command{Use: "set RUNTIME/MODEL", Short: "Save the judge model (must differ from your leads'; you choose, nothing is picked for you)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return saveJudge(cfg, cmd, args[0]) }}
	clr := &cobra.Command{Use: "clear", Short: "Forget the saved judge (the loop idles again)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return saveJudge(cfg, cmd, "") }}
	c := &cobra.Command{Use: "judge", Short: "Save or clear the judge model of the pursuit loop"}
	c.AddCommand(set, clr)
	clicore.WithJSONErrors(cfg.JSONOutput, set)
	clicore.WithJSONErrors(cfg.JSONOutput, clr)
	return c
}

func saveJudge(cfg *Deps, cmd *cobra.Command, pin string) error {
	if err := requireMissionOperator(cfg, "choose the pursuit judge model"); err != nil {
		return err
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := coderhost.NewStore(db.DB).SetSavedPursuitJudge(pin); err != nil {
		return err
	}
	return printQueueJSON(cmd, map[string]any{"savedJudge": pin, "note": restartNote})
}
