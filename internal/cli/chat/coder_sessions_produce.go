package chat

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions park|record`: the producer side of the autonomy backend. A coder turn parks a
// question about ITS OWN session or records a decision it made for it. They need the session
// token of a turn: the caller is the session the token names, there is no conversation argument,
// and the operator has no session of its own, so without a token they are refused (the operator
// answers questions with `questions answer` and reverts decisions with `decisions revert`).

func producerCall(cfg *Deps, verb string) (*masterCall, error) {
	if sessionTokenFromEnv() == "" {
		return nil, clicore.OperatorOnly("%s acts on the calling coder session's own state and needs the session token of a coder turn (%s)", verb, coderhost.SessionTokenEnv)
	}
	return openMasterCall(cfg)
}

// producerErr maps a producer error to a CLI error.
func producerErr(err error) error {
	switch {
	case errors.Is(err, coderhost.ErrModelDecides):
		return clicore.InvalidInput("the policy lets you settle this gap yourself: decide, then run `coder sessions record` with your choice, the alternatives and the reason (%v)", err)
	case errors.Is(err, coderhost.ErrPolicyReserves):
		return clicore.InvalidInput("%v", err)
	case errors.Is(err, coderhost.ErrTooManyOpen), errors.Is(err, coderhost.ErrNoConversation):
		return clicore.InvalidInput("%v", err)
	}
	return masterErr(err)
}

func newCoderSessionsParkCmd(cfg *Deps) *cobra.Command {
	var r coderhost.ParkRequest
	cmd := &cobra.Command{
		Use:   "park",
		Short: "Park a question about your own session when nobody present can answer it (needs the session token of a coder turn)",
		Long: "Writes a question about the calling session and puts the session into waiting_user once its turn ends; the answer " +
			"arrives later as a message. The policy decides who answers (the master, or the user without one). When the policy lets you " +
			"settle the gap yourself nothing is parked: decide and run `coder sessions record`. risky_action and missing_credential gaps " +
			"are never yours to decide and are never reversible. --default is applied after 30 minutes only where the policy allows a default " +
			"(never for those two classes).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mc, err := producerCall(cfg, "park")
			if err != nil {
				return err
			}
			defer mc.close()
			q, err := mc.store.ParkOwn(mc.caller, r)
			if err != nil {
				return producerErr(err)
			}
			return printQueueJSON(cmd, q)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.Class, "class", "", "Class of gap: "+classList())
	f.StringVar(&r.Question, "question", "", "The question")
	f.StringArrayVar(&r.Options, "option", nil, "An option you see (repeat)")
	f.StringVar(&r.Default, "default", "", "What to do if nobody answers in time, where the policy allows a default")
	f.BoolVar(&r.Reversible, "reversible", false, "The choice can be undone cheaply (never for risky_action or missing_credential)")
	_ = cmd.MarkFlagRequired("class")
	_ = cmd.MarkFlagRequired("question")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newCoderSessionsRecordCmd(cfg *Deps) *cobra.Command {
	var r coderhost.RecordRequest
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record a decision you made for your own session, so a person can review it (needs the session token of a coder turn)",
		Long: "Writes a ledger row, made by the model, for the calling session. It is refused when the policy does not let the model settle that " +
			"class of gap (then `park` it), and always for risky_action and missing_credential. It carries nothing out: it changes no access, " +
			"budget or setting and starts no turn.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mc, err := producerCall(cfg, "record")
			if err != nil {
				return err
			}
			defer mc.close()
			e, err := mc.store.RecordOwn(mc.caller, r)
			if err != nil {
				return producerErr(err)
			}
			return printQueueJSON(cmd, e)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.Class, "class", "", "Class of gap: "+classList())
	f.StringVar(&r.Question, "question", "", "What had to be decided")
	f.StringVar(&r.Choice, "choice", "", "What you chose")
	f.StringArrayVar(&r.Alternatives, "alternative", nil, "An alternative you did not choose (repeat)")
	f.StringVar(&r.Reason, "reason", "", "Why")
	f.BoolVar(&r.Reversible, "reversible", false, "The choice can be undone cheaply")
	_ = cmd.MarkFlagRequired("class")
	_ = cmd.MarkFlagRequired("choice")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func classList() string {
	out := ""
	for i, c := range coderhost.GapClasses {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}
