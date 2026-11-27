package hil

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/hilscrub"
	"github.com/monoes/mono-agent/internal/hilsend"
)

// everyProfile is pendingItems' profile for every profile's queue.
const everyProfile = ""

// pendingItems reads the pending items of profile, or of every profile
// (everyProfile, `hil pending --all` only), oldest first, each as `hil list`
// shows it.
func pendingItems(ctx context.Context, db *sql.DB, profile string) ([]hilItem, error) {
	where, args := `h.status = 'pending'`, []any{}
	if profile != everyProfile {
		where, args = where+` AND h.profile_id = ?`, append(args, profile)
	}
	// Older rows are scrubbed before they are shown, profile by profile.
	profiles := []string{profile}
	if profile == everyProfile {
		var err error
		if profiles, err = pendingProfiles(ctx, db); err != nil {
			return nil, err
		}
	}
	for _, p := range profiles {
		hilscrub.Ensure(ctx, db, p)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
		        h.readonly_data, h.editable_data, h.node_config, h.created_at, COALESCE(w.name,''),
		        COALESCE(h.profile_id,''), COALESCE(p.name,'')
		 FROM hil_pending h
		 LEFT JOIN workflows w ON w.id = h.workflow_id
		 LEFT JOIN profiles p ON p.id = h.profile_id
		 WHERE `+where+`
		 ORDER BY h.created_at ASC, h.rowid ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying HIL items: %w", err)
	}
	defer rows.Close()
	var items []hilItem
	var raws [][3]string
	for rows.Next() {
		var it hilItem
		var raw [3]string
		if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
			&it.Status, &raw[0], &raw[1], &raw[2], &it.CreatedAt, &it.WorkflowName, &it.ProfileID, &it.ProfileName); err != nil {
			return nil, fmt.Errorf("scanning HIL item: %w", err)
		}
		items, raws = append(items, it), append(raws, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating HIL items: %w", err)
	}
	rows.Close() // the lookups below need the connection
	for i := range items {
		it, raw := &items[i], raws[i]
		it.Digest, it.NoChatApprove = hilsend.Digest(raw[0], raw[1], raw[2]), twoStepItem(raw[2])
		// Rows the scrub could not rewrite are shown masked.
		for k := range raw {
			raw[k] = hilscrub.Mask(db, it.ProfileID, it.ID, raw[k])
		}
		_ = json.Unmarshal([]byte(raw[0]), &it.ReadonlyData)
		_ = json.Unmarshal([]byte(raw[1]), &it.EditableData)
		_ = json.Unmarshal([]byte(raw[2]), &it.NodeConfig)
		if pid, name, ok := commsync.LinkFor(ctx, db, it.ID); ok {
			it.PersonID, it.PersonName, it.HilRef = pid, name, it.ID
		}
		if send := hilsend.FromRow(it.ID, it.Status, raw[0], raw[1], raw[2]); send != nil {
			it.Kind, it.Send = "send", send
		}
	}
	return items, nil
}

func pendingProfiles(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT COALESCE(profile_id,'') FROM hil_pending WHERE status = 'pending'`)
	if err != nil {
		return nil, fmt.Errorf("querying HIL items: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scanning HIL item: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d Deps) newHILPendingCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List pending approvals of one profile, or of every profile (--all, operator only)",
		Long: "Lists what waits for approval. It only reads: decide an item with `hil approve|reject` or `hil resolve` " +
			"and the --profile it belongs to.\n\nWith --profile it lists that profile's items. With --all, and no " +
			"--profile, it lists every profile's items, each with its profile: the only way `hil` reads across profiles, " +
			"and refused to an agent.",
		Example: `  monoagentcli --profile work hil pending
  monoagentcli hil pending --all
  monoagentcli --json hil pending --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				// The turn-aware gate first, as `hil expire`: a damaged turn record refuses as operator_only too.
				if why := clicore.OperatorRefusal(opgate.Refusal); why != "" {
					return clicore.OperatorOnly("hil pending --all reads every profile's queue, which only the user may: %s; run it yourself in your terminal or the app", why)
				}
				if err := clicore.RequireOperator(d.RequireOperator, "list the approvals of every profile"); err != nil {
					return err
				}
				if d.ProfileID() != "" {
					return d.InvalidInput("--all lists every profile: leave out --profile, or leave out --all")
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			profile := everyProfile
			if !all {
				if profile = d.ProfileID(); profile == everyProfile {
					return d.InvalidInput("pass --profile <id|name>, or --all")
				}
			}
			items, err := pendingItems(cmd.Context(), db.DB, profile)
			if err != nil {
				return err
			}
			if opgate.Refusal() != "" {
				hideReplyTokens(cmd.Context(), db.DB, items)
			}
			if d.JSONOutput() {
				if items == nil {
					items = []hilItem{}
				}
				return d.WriteJSON(cmd.OutOrStdout(), items)
			}
			d.printPending(cmd, items)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Every profile's pending items, each with its profile (operator only; not with --profile)")
	return cmd
}

func (d Deps) printPending(cmd *cobra.Command, items []hilItem) {
	w := cmd.OutOrStdout()
	if len(items) == 0 {
		fmt.Fprintln(w, "No pending Human-in-Loop items.")
		return
	}
	table := d.NewTable(w, []string{"ID", "Profile", "Workflow", "Node", "Waiting"})
	for _, it := range items {
		profile, name := it.ProfileName, it.WorkflowName
		if profile == "" {
			profile = it.ProfileID
		}
		if name == "" {
			name = it.WorkflowID
		}
		created := it.CreatedAt
		if n := waitingNote(it.CreatedAt, time.Now()); n != "" {
			created = n
		}
		// The whole id: a short one is only unique within a profile.
		table.Append([]string{it.ID, truncateStr(hilsend.OneLine(profile), 24), truncateStr(hilsend.OneLine(name), 24),
			truncateStr(hilsend.OneLine(it.NodeName), 20), created})
	}
	table.Render()
	fmt.Fprintf(w, "\nTotal: %d pending item(s). Decide one with `monoagentcli --profile <profile> hil approve <id>` (or `hil reject <id>`).\n", len(items))
}
