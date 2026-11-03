package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The read-only verbs of the control room (see controlroom_types.go for the contract). They carry the same gate as the
// verbs that act: the feed shows the internals of a run, so an agent, a marker or a coder turn is refused as
// operator_only. They never write, and never start, approve, pause, resume or grant anything.

// now is the clock of generated_at; a test replaces it.
var now = time.Now

// readView asks the engine for the snapshot. unavailable is the reason of available:false ("" when there is a view);
// code is the error_code when that reason is "error". A failure to read is never the mode off: err is then non-nil and
// the caller prints the error document before returning it.
func (d Deps) readView(ctx context.Context, q store.ViewQuery) (v store.View, unavailable, code string, err error) {
	eng := d.engine
	if d.ReadEngine != nil {
		eng = func() Engine {
			if e := d.ReadEngine(); e != nil {
				return e
			}
			return d.engine()
		}
	}
	vr, ok := eng().(ViewReader)
	if !ok {
		return store.View{}, ReasonOff, "", nil
	}
	v, err = vr.ReadView(ctx, q)
	switch {
	case err == nil:
		return v, "", "", nil
	case errors.Is(err, ErrOff):
		return store.View{}, ReasonOff, "", nil
	case errors.Is(err, store.ErrNoRun):
		return store.View{}, ReasonNoRun, "", nil
	case errors.Is(err, store.ErrUnverifiable):
		// The run or charter row does not verify: report the badge, nothing else.
		return store.View{Chain: store.ChainState{Reason: store.ReasonDoesNotVerify}}, "", "", nil
	case errors.Is(err, ErrUnreadable):
		return store.View{}, ReasonError, ErrCodeDBUnavailable, err
	case errors.Is(err, store.ErrViewTimeout):
		return store.View{}, ReasonError, ErrCodeReadTimeout, err
	}
	return store.View{}, ReasonError, ErrCodeReadFailed, err
}

func (d Deps) newOverview() *cobra.Command {
	return &cobra.Command{
		Use:   "overview",
		Short: "Control room: the run's state, tree clusters, spend and integrity (read-only)",
		Long: "Control room: the run's state, tree clusters, spend and integrity (read-only; available:false when the mode is off, " +
			"there is no run, or the read failed: reason error and a non-zero exit). integrity.chain_verified is yes only when the audit " +
			"chain verified AND its head agrees with the stop witness; unanchored means consistent but not compared with the witness " +
			"(not healthy). Spend totals are not authenticated (spend.counters_authenticated is false). " + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorGate("read the open-ended run's control room"); err != nil {
				return err
			}
			v, why, code, err := d.readView(cmd.Context(), store.ViewQuery{FeedLimit: 1})
			o := unavailableOverview(why, code)
			if why == "" {
				o = BuildOverview(v, now().Unix())
			}
			if perr := printControlRoom(cmd.OutOrStdout(), d.json(), o, func(w io.Writer) { overviewText(w, o) }); perr != nil {
				return perr
			}
			return err // a failed read is the document above AND a non-zero exit
		},
	}
}

func (d Deps) newFeed() *cobra.Command {
	var before int64
	var limit int
	c := &cobra.Command{
		Use:   "feed",
		Short: "Control room: the decision feed, newest first, redacted (read-only)",
		Long: "Control room: the decision feed, newest first. Rows show the class, amounts and references of a decision, never " +
			"its text. Read-only; available:false when the mode is off, there is no run, or the read failed (reason error, non-zero exit). " + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorGate("read the open-ended run's decision feed"); err != nil {
				return err
			}
			if before < 0 || limit < 0 || limit > store.MaxFeedLimit {
				return clicore.InvalidInput("--limit must be 1..%d and --before not negative", store.MaxFeedLimit)
			}
			v, why, code, err := d.readView(cmd.Context(), store.ViewQuery{FeedBefore: before, FeedLimit: limit})
			p := unavailableFeed(why, code)
			if why == "" {
				p = BuildFeed(v, now().Unix())
			}
			if perr := printControlRoom(cmd.OutOrStdout(), d.json(), p, func(w io.Writer) { feedText(w, p) }); perr != nil {
				return perr
			}
			return err
		},
	}
	c.Flags().Int64Var(&before, "before", 0, "Page cursor: only rows with a lower seq (next_before of the previous page)")
	c.Flags().IntVar(&limit, "limit", store.DefaultFeedLimit, "Rows per page (1..200)")
	return c
}

func printControlRoom(out io.Writer, asJSON bool, doc any, text func(io.Writer)) error {
	if asJSON {
		return json.NewEncoder(out).Encode(doc)
	}
	text(out)
	return nil
}

func overviewText(w io.Writer, o Overview) {
	if !o.Available {
		fmt.Fprintf(w, "no control room (%s%s)\n", o.Reason, errSuffix(o.ErrorCode))
		return
	}
	fmt.Fprintf(w, "run %s: %s; chain verified: %s\n", o.Run.RunID, o.Run.State, o.Integrity.ChainVerified)
	if o.Tree != nil {
		fmt.Fprintf(w, "members: %d live of %d in %d kinds\n", o.Tree.Live, o.Tree.Total, len(o.Tree.Clusters))
	}
	if o.Spend != nil {
		fmt.Fprintf(w, "spend: %d micro used\n", o.Spend.UsedMicro)
	}
}

func feedText(w io.Writer, p FeedPage) {
	if !p.Available {
		fmt.Fprintf(w, "no control room (%s%s)\n", p.Reason, errSuffix(p.ErrorCode))
		return
	}
	if p.Integrity != nil && p.Integrity.ChainVerified != VerifiedYes {
		if p.Integrity.ChainVerified != VerifiedUnanchored {
			fmt.Fprintf(w, "the audit chain does not verify (%s): no rows shown\n", p.Integrity.ChainVerified)
			return
		}
		fmt.Fprintf(w, "the audit chain is consistent but unanchored (%s): not compared with the witness\n", p.Integrity.Reason)
	}
	for _, r := range p.Items {
		fmt.Fprintf(w, "%d %s %s %s\n", r.Seq, r.Class, r.Kind, r.Result)
	}
}

func errSuffix(code string) string {
	if code == "" {
		return ""
	}
	return ": " + code
}
