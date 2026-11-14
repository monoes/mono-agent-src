package chat

import (
	"fmt"
	"os"
	"strings"

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

// coderHostPursuitStatus prints the one place the app reads. JSON keys (schema_version 1; keys are added, never renamed):
//
//	savedOperatorOff  the SAVED row (`coder host pursuit off`); null with readError when the row could not be read (never a guess)
//	savedJudge        the saved pin ("" none)
//	readError         why the saved settings could not be read, else absent
//	pendingRestart    the saved settings differ from the running host: a restart would change its loop
//	enabled, missing, pinSource, operatorOff, reason   the RUNNING host's loop (from host.json); when no host published one:
//	                  enabled false, missing/pinSource derived from the saved settings, reason says so
//	host              {found, alive, pursuit}: the raw host facts
//	notice, pauseCommand   the first-run notice (the published wake-up cap when a host runs) and the verb that pauses one mission
func coderHostPursuitStatus(cfg *Deps, cmd *cobra.Command) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	st := coderhost.NewStore(db.DB)
	var savedOff *bool
	var readErr string
	off, offErr := st.PursuitOperatorOff()
	pin, pinErr := st.SavedPursuitJudge()
	if offErr != nil {
		readErr = offErr.Error()
	} else {
		savedOff = &off
	}
	if pinErr != nil {
		readErr = pinErr.Error()
	}
	host, found, alive := coderhost.ReadHostPursuitState(clicore.ExpandPath(cfg.DBPath()))
	out := map[string]any{"schema_version": 1, "savedOperatorOff": savedOff, "savedJudge": pin, "pauseCommand": coderhost.PursuitPauseCommand,
		"host": map[string]any{"found": found, "alive": alive, "pursuit": host}}
	if readErr != "" {
		out["readError"] = readErr
	}
	// host.json is display-only and any process of the user can write it: its values are used only for a live host whose facts pass
	// the validity check; otherwise the default caps show and the loop is not reported as running.
	trusted := found && alive && coderhost.ValidHostPursuitInfo(host)
	out["hostTrusted"] = trusted
	if !trusted {
		out["host"] = map[string]any{"found": found, "alive": alive, "pursuit": coderhost.PursuitHostInfo{}}
	}
	pending := trusted && readErr == "" && coderhost.PendingPursuitRestart(off, pin, host)
	out["pendingRestart"] = pending
	wake := 0.0
	var pinSource any
	switch {
	case trusted:
		out["enabled"], out["missing"], out["operatorOff"], out["reason"] = host.Enabled, host.Missing, host.OperatorOff, host.Reason
		if host.PinSource != "" {
			pinSource = host.PinSource
		}
		wake = host.WakeCapUSD
	default:
		out["enabled"], out["operatorOff"] = false, nil
		out["reason"] = "no running coder host has published valid pursuit state (none is running, it is older than this field, or its status file is not trustworthy); the default caps are shown"
		out["missing"] = ""
		if pin == "" {
			out["missing"] = coderhost.PursuitMissingJudgePin
		} else {
			pinSource = "saved"
		}
	}
	out["pinSource"] = pinSource
	notice := coderhost.PursuitNotice(wake)
	out["notice"] = notice
	if cfg.JSONOutput() {
		return printQueueJSON(cmd, out)
	}
	w := cmd.OutOrStdout()
	state := "on by default"
	switch {
	case savedOff == nil:
		state = "unknown (" + readErr + ")"
	case off:
		state = "OFF (the operator turned it off)"
	}
	fmt.Fprintf(w, "saved switch: %s\nsaved judge: %q\n", state, pin)
	if trusted {
		fmt.Fprintln(w, "host: "+pursuitLoopLine(host))
	}
	if pending {
		fmt.Fprintln(w, "restart pending: the saved settings differ from what the running coder host started with; restart the coder host to apply them")
	}
	fmt.Fprintln(w, notice)
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
	out := map[string]any{"savedJudge": pin, "note": restartNote}
	if env := strings.TrimSpace(os.Getenv(envPursuitJudge)); env != "" {
		out["envOverride"] = env
		out["warning"] = envPursuitJudge + " is set in this shell; a host started with it uses that pin and ignores the saved one"
	}
	return printQueueJSON(cmd, out)
}
