package apiconfig

import (
	"errors"
	"fmt"
	"slices"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/openaiapi"
)

// The document `api models --json` prints is the one the MCP tool api_models_list
// returns (openaiapi.ModelsReport), so its types and the rules that fill them live in
// internal/openaiapi.

// AutoNote is the line the text output gives for it.
func AutoNote(a openaiapi.AutoReport) string {
	if !a.Available {
		return "off, it needs " + a.Missing
	}
	note := "available"
	if a.Candidates > 0 {
		plural := "s"
		if a.Candidates == 1 {
			plural = ""
		}
		note += fmt.Sprintf(" (Jev picks among the %d model%s it may use here, up to %s", a.Candidates, plural, a.Confinement)
		if a.HeldBack > 0 {
			note += fmt.Sprintf("; %d more are served here but above --auto-confinement", a.HeldBack)
		}
		note += ")"
	}
	if a.KeySource == "env" {
		note += "; the Jev key is this shell's TYPESAFE_API_KEY, and a running server reads its own environment"
	}
	return note
}

// toolsNote is the line under the table for the TOOLS column, which is the models that can serve
// tool calling on this machine and not what a given key may do.
const toolsNote = "tools: a model serves them only where monomind can apply the sandbox every turn with tools requires; " +
	"a key created with --context is refused them unless --context-confinement (and the listener's policy) is above chat-only"

// NewModelsCommand is `api models`.
func (d Deps) NewModelsCommand() *cobra.Command {
	var forListener, confinement, contextConfinement, autoConfinement string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models /v1/models would serve, with each one's confinement class",
		Long: "Lists every model of the installed agent runtimes with its confinement class (chat-only, sandboxed or " +
			"unconfined) and whether the confinement policy of a loopback or a network listener allows it. " +
			"The policy is --confinement, else MONOAGENT_API_CONFINEMENT, else the listener's default: any on " +
			"loopback, chat-only on a network bind. A key created with --context is held to --context-confinement, " +
			"else MONOAGENT_API_CONTEXT_CONFINEMENT, else chat-only, and never above the listener's policy. " +
			"The auto model is held to --auto-confinement, else MONOAGENT_API_AUTO_CONFINEMENT, else chat-only, the same way. " +
			"IMAGES (capabilities in --json) says which models make images, from MONOAGENT_API_IMAGE_RUNTIMES, else codex and antigravity. " +
			"TOOLS says which models serve tool calling, from MONOAGENT_API_TOOL_RUNTIMES, else claude and codex (a runtime that is not " +
			"chat-only also needs monomind to run it read-only, and every turn with tools requires monomind's sandbox, " +
			"agent-exec-sandbox, to be applicable to its runtime). Either list can be none, which switches that off. " +
			"A key created with --context is refused tools whatever this column says, unless --context-confinement is above chat-only " +
			"(and the listener's policy is too: it is held to the lower of the two).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := d.representativeAddr(forListener)
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			getenv, err := d.SavedEnv(cmd.Context(), db.DB) // this shell's environment, with the saved settings under it
			if err != nil {
				return err
			}
			policy, err := d.effectivePolicy(addr, confinement, getenv)
			if err != nil {
				return err
			}
			if policy.ContextMax, err = d.effectiveContextMax(contextConfinement, getenv); err != nil {
				return err
			}
			if policy.AutoMax, err = d.effectiveAutoMax(autoConfinement, getenv); err != nil {
				return err
			}
			imageRuntimes, err := openaiapi.EffectiveImageRuntimes(getenv)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			toolRuntimes, err := openaiapi.EffectiveToolRuntimes(getenv)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			forContext := policy.ForContextKey()
			models, err := openaiapi.LoadModels(cmd.Context(), db.DB)
			if err != nil {
				return err
			}
			out := openaiapi.NewModelsReport(openaiapi.ModelsReportInput{
				For: forListener, Policy: policy, Source: openaiapi.ReportSourceShell, Models: models, ImageRuntimes: imageRuntimes, ToolRuntimes: toolRuntimes,
				Auto: openaiapi.DefaultAuto(db.DB).Status(cmd.Context(), d.ProfileID()),
			})
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), out)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Confinement policy for a %s listener: %s (keys created with --context: %s)\n", forListener, policy, forContext)
			fmt.Fprint(w, "From this shell's flags, environment and saved settings: a running server may be set up differently (`monoagentcli api status` shows what a running daemon applies).\n\n")
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "MODEL\tCONFINEMENT\tVALIDATED\tSERVED\tCONTEXT KEY\tAUTO\tIMAGES\tTOOLS")
			for _, m := range out.Models {
				served, withContext, withAuto, withImages, withTools := "yes", "yes", "yes", "no", "no"
				if !m.Allowed {
					served = "no (policy)"
				}
				if !m.ContextAllowed {
					withContext = "no"
				}
				if !m.AutoAllowed {
					withAuto = "no"
				}
				if slices.Contains(m.Capabilities, "image") {
					withImages = "yes"
				}
				if slices.Contains(m.Capabilities, "tools") {
					withTools = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%v\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.Confinement, m.Validated, served, withContext, withAuto, withImages, withTools)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(w, "\nauto: %s\n", AutoNote(out.Auto))
			fmt.Fprintln(w, toolsNote)
			return nil
		},
	}
	cmd.Flags().StringVar(&forListener, "for", "loopback", "Evaluate the policy of a loopback or a network listener")
	cmd.Flags().StringVar(&confinement, "confinement", "", "Confinement maximum: chat-only, sandboxed or any")
	cmd.Flags().StringVar(&contextConfinement, "context-confinement", "", "Strongest class a key created with --context may use: chat-only, sandboxed or any")
	cmd.Flags().StringVar(&autoConfinement, "auto-confinement", "", "Strongest class the auto model may pick: chat-only, sandboxed or any")
	return cmd
}

// The four functions below are the rules of internal/openaiapi (the MCP tool
// api_models_list applies them too), with this command's flag names in the errors.

// representativeAddr is a bind address of the given kind, for evaluating
// the per-listener default policy.
func (d Deps) representativeAddr(kind string) (string, error) {
	addr, err := openaiapi.ListenerAddr(kind)
	if err != nil {
		return "", d.InvalidInput("--for %v", err)
	}
	return addr, nil
}

// effectiveContextMax is the strongest class a key created with --context may
// use: the explicit value (a flag), else MONOAGENT_API_CONTEXT_CONFINEMENT,
// else chat-only.
func (d Deps) effectiveContextMax(explicit string, getenv func(string) string) (openaiapi.Class, error) {
	c, err := apiconfig.EffectiveContextMax(explicit, getenv)
	return c, d.AsCLIError(err)
}

// effectiveAutoMax is the strongest class the auto model may pick: the explicit
// value (a flag), else MONOAGENT_API_AUTO_CONFINEMENT, else chat-only.
func (d Deps) effectiveAutoMax(explicit string, getenv func(string) string) (openaiapi.Class, error) {
	c, err := apiconfig.EffectiveAutoMax(explicit, getenv)
	return c, d.AsCLIError(err)
}

// effectivePolicy is the confinement policy of a listener bound to addr: the
// explicit value (a flag), else MONOAGENT_API_CONFINEMENT, else the default
// for that kind of bind.
func (d Deps) effectivePolicy(addr, explicit string, getenv func(string) string) (openaiapi.Policy, error) {
	p, err := apiconfig.EffectivePolicy(addr, explicit, getenv)
	return p, d.AsCLIError(err)
}

// AsCLIError gives the errors of internal/apiconfig the exit codes of the CLI, in one place: a
// value of a flag, a variable or a saved setting that fails its rule is invalid input (exit 3),
// and so is a saved row that cannot be read (apiconfig.ErrDamaged): invalid saved data the user
// can fix, whose message starts with apiconfig.DamagedMessage and names the repair, so that a
// caller that has only the exit code and the last line of stderr can offer it. Each keeps the
// message it has. Any other error is left as it is, a row in a newer format (ErrTooNew) and a
// failing database included: neither is damage.
func AsCLIError(err error) error {
	var input *apiconfig.InputError
	var invalid *apiconfig.ValidationError
	if errors.As(err, &input) || errors.As(err, &invalid) || errors.Is(err, apiconfig.ErrDamaged) {
		return clicore.InvalidInput("%v", err)
	}
	return err
}
