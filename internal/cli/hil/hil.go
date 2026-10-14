package hil

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/spf13/cobra"
)

func (d Deps) newHILCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hil",
		Short: "List, approve, or reject Human-in-Loop items",
		Long: "Human-in-Loop (HIL) nodes pause a running workflow until a person reviews and " +
			"approves or rejects the data. This command exposes that queue to headless/agent use — " +
			"the same operations as the desktop app's Human in Loop page.",
	}
	cmd.AddCommand(
		d.newHILListCmd(),
		d.newHILApproveCmd(),
		d.newHILRejectCmd(),
		d.newHILResolveCmd(), d.newHILSendsCmd(), d.newHILShowCmd(), d.newHILEditCmd(),
		d.newHILAttachmentCmd(), d.newHILRecipientCmd(), d.newHILAllowCmd(),
	)
	return cmd
}

type hilItem struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	NodeID       string                 `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Status       string                 `json:"status"`
	ReadonlyData map[string]interface{} `json:"readonly_data"`
	EditableData map[string]interface{} `json:"editable_data"`
	NodeConfig   map[string]interface{} `json:"node_config"`
	CreatedAt    string                 `json:"created_at"`
	// Kind is "send" for an outbound send an agent prepared; Send is then its
	// approval card (hilsend.Payload). Ordinary workflow items have neither.
	Kind string           `json:"kind,omitempty"`
	Send *hilsend.Payload `json:"send,omitempty"`
	// Suggestion is TypeSafe Jev's stored suggestion (--suggest only).
	Suggestion *hilsuggest.Suggestion `json:"suggestion,omitempty"`
}

func (d Deps) newHILListCmd() *cobra.Command {
	var suggest, resuggest bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending Human-in-Loop items",
		Long: "List pending Human-in-Loop items.\n\n" +
			"--suggest adds TypeSafe Jev's suggestion to each item (approve, reject or needs_human with " +
			"its probability p, and a low/medium/high risk). A suggestion stored on the item (by the " +
			"node's auto_decide, or by an earlier --suggest) is reused; a missing one is computed — one " +
			"request per item — and stored, when the profile enabled the surface (jev enable hil) and " +
			"a key resolves. --resuggest recomputes. Suggestions never approve or reject anything.",
		Example: `  monoagentcli hil list
  monoagentcli --json hil list
  monoagentcli --json hil list --suggest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(
				`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
				        h.readonly_data, h.editable_data, h.node_config, h.created_at, COALESCE(w.name,'')
				 FROM hil_pending h
				 LEFT JOIN workflows w ON w.id = h.workflow_id
				 WHERE h.status = 'pending' AND h.profile_id = ?
				 ORDER BY h.created_at ASC`,
				d.ProfileID(),
			)
			if err != nil {
				return fmt.Errorf("querying HIL items: %w", err)
			}
			defer rows.Close()

			var items []hilItem
			for rows.Next() {
				var it hilItem
				var roRaw, edRaw, cfgRaw string
				if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
					&it.Status, &roRaw, &edRaw, &cfgRaw, &it.CreatedAt, &it.WorkflowName); err != nil {
					return fmt.Errorf("scanning HIL item: %w", err)
				}
				_ = json.Unmarshal([]byte(roRaw), &it.ReadonlyData)
				_ = json.Unmarshal([]byte(edRaw), &it.EditableData)
				_ = json.Unmarshal([]byte(cfgRaw), &it.NodeConfig)
				if send := hilsend.FromRow(it.ID, it.Status, roRaw, edRaw, cfgRaw); send != nil {
					it.Kind, it.Send = "send", send
				}
				items = append(items, it)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating HIL items: %w", err)
			}
			rows.Close()

			if suggest || resuggest {
				suggestHILItems(cmd.Context(), db.DB, d.ProfileID(), items, resuggest)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if items == nil {
					items = []hilItem{}
				}
				return enc.Encode(items)
			}

			if len(items) == 0 {
				fmt.Println("No pending Human-in-Loop items.")
				return nil
			}

			header := []string{"ID", "Workflow", "Node", "Created"}
			if suggest || resuggest {
				header = append(header, "Suggestion")
			}
			table := d.NewTable(os.Stdout, header)
			for _, it := range items {
				shortID := it.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				name := it.WorkflowName
				if name == "" {
					name = it.WorkflowID
				}
				row := []string{shortID, truncateStr(name, 24), truncateStr(it.NodeName, 20), it.CreatedAt}
				if suggest || resuggest {
					sug := ""
					if s := it.Suggestion; s != nil {
						sug = fmt.Sprintf("%s p=%.2f risk=%s", s.Choice, s.P, s.Risk)
					}
					row = append(row, sug)
				}
				table.Append(row)
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d pending item(s). Approve with `hil approve <id>` or reject with `hil reject <id>`.\n", len(items))
			return nil
		},
	}
	cmd.Flags().BoolVar(&suggest, "suggest", false, "Add TypeSafe Jev's suggestion to each item (computed and stored when missing; needs 'jev enable hil')")
	cmd.Flags().BoolVar(&resuggest, "resuggest", false, "Recompute and store every suggestion (implies --suggest)")
	return cmd
}

// hilSuggestTimeout bounds one `hil list --suggest` run's Jev calls.
const hilSuggestTimeout = 60 * time.Second

// suggestHILItems fills items[i].Suggestion: the one stored in the row's
// node_config unless resuggest, else — when the profile enabled surface
// "hil" and a key resolves — a fresh one (one request per item), stored
// back into node_config. It only ever writes node_config of rows still
// pending, never their status. Problems are warnings on stderr: the list
// itself must not fail because Jev is unavailable.
func suggestHILItems(ctx context.Context, db *sql.DB, profileID string, items []hilItem, resuggest bool) {
	var todo []int
	for i := range items {
		if s, ok := storedHILSuggestion(items[i].NodeConfig); ok && !resuggest {
			items[i].Suggestion = s
			continue
		}
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return
	}
	if !jevconf.Enabled(db, profileID, jevconf.HIL) {
		fmt.Fprintln(os.Stderr, "warning: TypeSafe Jev suggestions are off for this profile — enable them with `monoagentcli jev enable hil`")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := jevconf.NewClient(ctx, db, profileID, "", "", jevconf.HIL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: no suggestions: %v\n", err)
		return
	}
	ins := make([]hilsuggest.Input, len(todo))
	for k, i := range todo {
		policy, _ := items[i].NodeConfig["policy"].(string)
		ins[k] = hilsuggest.Input{Readonly: items[i].ReadonlyData, Editable: items[i].EditableData, Policy: policy}
	}
	jctx, cancel := context.WithTimeout(ctx, hilSuggestTimeout)
	defer cancel()
	sugs, errs := hilsuggest.SuggestAll(jctx, client, ins)
	for k, i := range todo {
		if errs[k] != nil {
			fmt.Fprintf(os.Stderr, "warning: no suggestion for %s: %v\n", items[i].ID, errs[k])
			continue
		}
		s := sugs[k]
		items[i].Suggestion = &s
		if items[i].NodeConfig == nil {
			items[i].NodeConfig = map[string]interface{}{}
		}
		items[i].NodeConfig["suggestion"] = s
		raw, err := json.Marshal(items[i].NodeConfig)
		if err == nil {
			_, err = db.ExecContext(ctx, `UPDATE hil_pending SET node_config = ? WHERE id = ? AND status = 'pending' AND profile_id = ?`,
				string(raw), items[i].ID, profileID)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: storing the suggestion for %s: %v\n", items[i].ID, err)
		}
	}
}

// storedHILSuggestion decodes node_config.suggestion.
func storedHILSuggestion(nc map[string]interface{}) (*hilsuggest.Suggestion, bool) {
	raw, ok := nc["suggestion"]
	if !ok || raw == nil {
		return nil, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var s hilsuggest.Suggestion
	if err := json.Unmarshal(b, &s); err != nil || strings.TrimSpace(s.Choice) == "" {
		return nil, false
	}
	return &s, true
}

// fullHILID expands the short id `hil list` prints to the id of the one
// pending item it starts; anything else is returned as given.
func (d Deps) fullHILID(id string) string {
	db, err := d.OpenDB()
	if err != nil {
		return id
	}
	defer db.Close()
	rows, err := db.DB.Query(`SELECT id FROM hil_pending WHERE status = 'pending' AND profile_id = ? AND substr(id, 1, ?) = ? LIMIT 2`,
		d.ProfileID(), len(id), id)
	if err != nil {
		return id
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var full string
		if rows.Scan(&full) == nil {
			ids = append(ids, full)
		}
	}
	if len(ids) == 1 {
		return ids[0]
	}
	return id
}

func (d Deps) newHILApproveCmd() *cobra.Command {
	var editedData string
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a pending Human-in-Loop item",
		Long:  "Approve a pending item, optionally overriding its editable data with a JSON object via --data.",
		Example: `  monoagentcli hil approve 1a2b3c4d
  monoagentcli hil approve 1a2b3c4d --data '{"caption":"edited text"}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			args[0] = d.fullHILID(args[0])
			if editedData == "" {
				editedData = "{}"
			}
			if done, err := d.approveIfSend(cmd, args[0], true, editedData); done {
				return err
			}
			var check map[string]interface{}
			if err := json.Unmarshal([]byte(editedData), &check); err != nil {
				return fmt.Errorf("--data is not valid JSON: %w", err)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			res, err := db.DB.Exec(
				`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='pending' AND profile_id = ?`,
				editedData, args[0], d.ProfileID(),
			)
			if err != nil {
				return fmt.Errorf("approving HIL item: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return d.NotFound("HIL item %q not found or already resolved", args[0])
			}
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": args[0], "status": "approved"})
			}
			fmt.Printf("Approved HIL item %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&editedData, "data", "", "JSON object overriding the item's editable data")
	return cmd
}

func (d Deps) newHILRejectCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "reject <id>",
		Short:   "Reject a pending Human-in-Loop item (the workflow errors out)",
		Example: `  monoagentcli hil reject 1a2b3c4d`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			args[0] = d.fullHILID(args[0])
			if done, err := d.approveIfSend(cmd, args[0], false, ""); done {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			res, err := db.DB.Exec(
				`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='pending' AND profile_id = ?`,
				args[0], d.ProfileID(),
			)
			if err != nil {
				return fmt.Errorf("rejecting HIL item: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return d.NotFound("HIL item %q not found or already resolved", args[0])
			}
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": args[0], "status": "rejected"})
			}
			fmt.Printf("Rejected HIL item %s\n", args[0])
			return nil
		},
	}
}
