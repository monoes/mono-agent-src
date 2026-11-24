package hil

import (
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/hilsend"
)

// A card that waits is never rejected on its own: approval never happens by itself, and neither does
// a refusal. What the commands do instead is say how long it has waited (a nudge from one day), warn
// before an old card is approved (staleDays), and let the operator expire one by hand.
const (
	nudgeAfter = 24 * time.Hour
	staleDays  = 7
)

// SendApprovalOffWarning is the loud line `hil sends` prints while the profile's send gate is off.
const SendApprovalOffWarning = "WARNING: send approval is OFF for this profile (sendApproval=false): agent sends and publications go out with no card."

// waited is how long a card made at createdAt (RFC3339) has waited, 0 when it cannot be read.
func waited(createdAt string, now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", createdAt) // SQLite's CURRENT_TIMESTAMP, UTC
	}
	if err != nil || t.After(now) {
		return 0
	}
	return now.Sub(t)
}

// waitingNote is "waiting N day(s)" for a pending card past nudgeAfter, "" before.
func waitingNote(createdAt string, now time.Time) string {
	w := waited(createdAt, now)
	if w < nudgeAfter {
		return ""
	}
	days := int(w / (24 * time.Hour))
	if days == 1 {
		return "waiting 1 day"
	}
	return fmt.Sprintf("waiting %d days", days)
}

// staleWarning says why approving p now deserves a second look, "" for a card younger than staleDays.
func staleWarning(p hilsend.Payload, now time.Time) string {
	if p.State != hilsend.StatePending || waited(p.CreatedAt, now) < staleDays*24*time.Hour {
		return ""
	}
	return fmt.Sprintf("Warning: this card has waited %s; the recipient's context may have changed since it was written. "+
		"Read it again, or expire it with `hil expire %s`.", waitingNote(p.CreatedAt, now)[len("waiting "):], p.ID)
}

// printSendsHeader writes the gate-off warning before a `hil sends` listing.
func printSendsHeader(w io.Writer, db *sql.DB) {
	if !hilsend.Enabled(db) {
		fmt.Fprintln(w, SendApprovalOffWarning)
	}
}

func (d Deps) newHILExpireCmd() *cobra.Command {
	var olderDays int
	cmd := &cobra.Command{
		Use:   "expire [id...]",
		Short: "Reject stale pending sends by hand (operator only)",
		Long: "Rejects pending send items so they can never be sent: the ones named, or with --older-than-days N every " +
			"pending send that has waited that long. Nothing expires by itself; a card that waits is only flagged " +
			"in `hil sends` and warned about when approved after " + fmt.Sprint(staleDays) + " days.\n\n" + clicore.OperatorOnlyHelp,
		Example: `  monoagentcli hil expire 7c1e
  monoagentcli hil expire --older-than-days 14`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 0) == (olderDays <= 0) {
				return d.InvalidInput("give card ids, or --older-than-days N, not both and not neither")
			}
			if err := clicore.RequireOperator(d.RequireOperator, "expire a send"); err != nil {
				return err
			}
			return d.withSendDB(func(_ *sql.DB, st hilsend.Store) error {
				ids := args
				if olderDays > 0 {
					items, err := st.List(cmd.Context(), hilsend.StatePending)
					if err != nil {
						return err
					}
					for _, p := range items {
						if waited(p.CreatedAt, time.Now()) >= time.Duration(olderDays)*24*time.Hour {
							ids = append(ids, p.ID)
						}
					}
				}
				expired := []hilsend.Payload{}
				for _, id := range ids {
					p, err := st.Get(cmd.Context(), id)
					if err != nil {
						return d.hilSendError(err)
					}
					if p.State != hilsend.StatePending {
						return d.InvalidInput("send item %s is %s, only a pending one can be expired", p.ID, p.State)
					}
					p, err = hilsend.Resolve(cmd.Context(), st, p.ID, false, hilsend.Edit{}, false, d.SendRunner())
					if err != nil {
						return d.hilSendError(err)
					}
					expired = append(expired, p)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), expired)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Expired %d send(s).\n", len(expired))
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&olderDays, "older-than-days", 0, "Expire every pending send that has waited at least this many days")
	return cmd
}
