package task

import (
	"errors"
	"fmt"
	"math"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// reservationJSON is a reservation as the CLI prints it. The owner id is a handle the holder keeps, so it is not shown.
type reservationJSON struct {
	ID         int64   `json:"id"`
	Session    int64   `json:"session"`
	Worker     string  `json:"worker"`
	State      string  `json:"state"`
	AmountUSD  float64 `json:"amount_usd"`
	ChargedUSD float64 `json:"charged_usd"`
	HeldUSD    float64 `json:"held_usd"`
	Bounded    bool    `json:"bounded"`
	LeaseUntil string  `json:"lease_until"`
	CreatedAt  string  `json:"created_at"`
	EndedAt    string  `json:"ended_at,omitempty"`
	EndNote    string  `json:"end_note,omitempty"`
}

func toReservationJSON(r tasks.Reservation) reservationJSON {
	return reservationJSON{ID: r.ID, Session: r.SessionID, Worker: r.WorkerRef, State: r.State, AmountUSD: r.AmountUSD,
		ChargedUSD: r.ChargedUSD, HeldUSD: r.Held(), Bounded: r.Bounded, LeaseUntil: r.LeaseUntil, CreatedAt: r.CreatedAt,
		EndedAt: r.EndedAt, EndNote: r.EndNote}
}

// reservationErr is taskErr, plus the answer for a reservation id that is not there (or is another profile's).
func (d Deps) reservationErr(err error) error {
	if errors.Is(err, tasks.ErrReservationLost) {
		return d.NotFound("%v", err)
	}
	return d.taskErr(err)
}

func (d Deps) newSessionReservationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reservation",
		Short: "See and settle the money workers hold out of a session's budget",
		Long: `A worker holds part of its session's budget while it runs (a reservation). If the worker dies, the row
expires and keeps holding what is left of it until the operator says what it cost: reservation resolve ID --usd N.
list shows a session's reservations (the operator, or an agent bound to that session); resolve is the operator's.`,
	}
	cmd.AddCommand(d.newSessionReservationListCmd(), d.newSessionReservationResolveCmd())
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

func (d Deps) newSessionReservationListCmd() *cobra.Command {
	return d.oneSession("list SESSION", "List the reservations of a session, newest first (read-only)",
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			rs, err := store.SessionReservations(cmd.Context(), p.ID, id, d.callerFor(flagAs(cmd)).actor)
			if err != nil {
				return d.reservationErr(err)
			}
			out := make([]reservationJSON, 0, len(rs))
			for _, r := range rs {
				out = append(out, toReservationJSON(r))
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "session": id, "reservations": out})
			}
			w := cmd.OutOrStdout()
			printProfileLine(w, p)
			if len(out) == 0 {
				fmt.Fprintln(w, "No reservations.")
				return nil
			}
			for _, r := range out {
				fmt.Fprintf(w, "#%d  %-8s %s of %s charged, %s held  %s\n", r.ID, r.State, money(r.ChargedUSD), money(r.AmountUSD), money(r.HeldUSD), taskCut(r.Worker, 60))
			}
			return nil
		})
}

func (d Deps) newSessionReservationResolveCmd() *cobra.Command {
	var usd float64
	var note string
	var actor tasks.Actor
	cmd := d.operatorSession("resolve ID --usd N [--note TEXT]", "Say what an expired reservation cost, to charge it and release its hold (you only)", "resolve a reservation", &actor,
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			if !cmd.Flags().Changed("usd") {
				return d.InvalidInput("give what the reservation cost in total with --usd N (0 charges nothing more)")
			}
			if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd > tasks.MaxBudgetUSD {
				return d.InvalidInput("--usd is an amount in dollars from 0 to %.0f", tasks.MaxBudgetUSD)
			}
			// A live row whose lease lapsed past the grace is presumed dead: sweep it so it can be resolved.
			if _, err := store.ExpireStale(cmd.Context(), p.ID, 0); err != nil {
				return d.reservationErr(err)
			}
			if err := store.ResolveReservation(cmd.Context(), p.ID, id, usd, note, actor); err != nil {
				return d.reservationErr(err)
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "reservation": id, "state": tasks.ReservationResolved, "charged_usd": usd})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Resolved reservation #%d: %s charged in total.\n", id, money(usd))
			return nil
		})
	cmd.Flags().Float64Var(&usd, "usd", 0, "What the reservation cost in total, in dollars (not below what it already charged; 0 charges nothing more)")
	cmd.Flags().StringVar(&note, "note", "", "Where the figure comes from (kept on the reservation)")
	return cmd
}
