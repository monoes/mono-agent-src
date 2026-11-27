package hil

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/monoes/mono-agent/internal/hilscrub"
	"io"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/workflow"
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
		d.newHILExpireCmd(), d.newHILPendingCmd(),
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
	// Digest fingerprints the item's content (hilsend.Digest), for `hil
	// approve|reject --if-digest`.
	Digest string `json:"digest,omitempty"`
	// Redacted: a reply link's token was hidden in this item, so what is shown
	// is not all there is.
	Redacted bool `json:"redacted,omitempty"`
	// NoChatApprove: the item can only be approved on the approvals page.
	NoChatApprove bool `json:"no_chat_approve,omitempty"`
	// PersonID, PersonName and HilRef are set when the item has a row in
	// Communications (hil_ref is this item's id), so the GUI can link to both.
	PersonID   string `json:"person_id,omitempty"`
	PersonName string `json:"person_full_name,omitempty"`
	HilRef     string `json:"hil_ref,omitempty"`
	// Kind is "send" for an outbound send an agent prepared; Send is then its
	// approval card (hilsend.Payload). Ordinary workflow items have neither.
	Kind string           `json:"kind,omitempty"`
	Send *hilsend.Payload `json:"send,omitempty"`
	// Suggestion is TypeSafe Jev's stored suggestion (--suggest only).
	Suggestion *hilsuggest.Suggestion `json:"suggestion,omitempty"`
	// ProfileID and ProfileName say whose item it is (`hil pending` only).
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

func (d Deps) newHILListCmd() *cobra.Command {
	var suggest, resuggest, redact bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending Human-in-Loop items",
		Long: "List pending Human-in-Loop items.\n\n" +
			"--suggest adds TypeSafe Jev's suggestion to each item (approve, reject or needs_human with " +
			"its probability p, and a low/medium/high risk). A suggestion stored on the item (by the " +
			"node's auto_decide, or by an earlier --suggest) is reused; a missing one is computed — one " +
			"request per item — and stored, when the profile enabled the surface (jev enable hil) and " +
			"a key resolves. --resuggest recomputes. Suggestions never approve or reject anything.\n\n" +
			"Privacy: Jev is not local. An item's field text (at most 8000 characters in all, 2000 per field) is " +
			"sent to the decision provider the profile uses, TypeSafe's hosted service unless another provider was " +
			"added, so an item that holds email or DM text leaves this machine. `jev enable hil` is the operator's " +
			"consent for that, it is off by default, and only the operator can switch it on.",
		Example: `  monoagentcli hil list
  monoagentcli --json hil list
  monoagentcli --json hil list --suggest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			hilscrub.Ensure(cmd.Context(), db.DB, d.ProfileID())
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
				it.Digest, it.NoChatApprove = hilsend.Digest(roRaw, edRaw, cfgRaw), twoStepItem(cfgRaw)
				// Rows the scrub could not rewrite are shown masked.
				prof := d.ProfileID()
				roRaw, edRaw, cfgRaw = hilscrub.Mask(db.DB, prof, it.ID, roRaw), hilscrub.Mask(db.DB, prof, it.ID, edRaw), hilscrub.Mask(db.DB, prof, it.ID, cfgRaw)
				_ = json.Unmarshal([]byte(roRaw), &it.ReadonlyData)
				_ = json.Unmarshal([]byte(edRaw), &it.EditableData)
				_ = json.Unmarshal([]byte(cfgRaw), &it.NodeConfig)
				if pid, name, ok := commsync.LinkFor(cmd.Context(), db.DB, it.ID); ok {
					it.PersonID, it.PersonName, it.HilRef = pid, name, it.ID
				}
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
			if redact || opgate.Refusal() != "" {
				hideReplyTokens(cmd.Context(), db.DB, items)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
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
			if n := waitingNote(items[0].CreatedAt, time.Now()); n != "" { // oldest first
				fmt.Fprintf(os.Stderr, "\nThe oldest item (%s) is %s. Nothing is decided on its own: approve or reject it.\n", items[0].ID, n)
			}
			fmt.Fprintf(os.Stderr, "\nTotal: %d pending item(s). Approve with `hil approve <id>` or reject with `hil reject <id>`.\n", len(items))
			return nil
		},
	}
	cmd.Flags().BoolVar(&redact, "redact", false, "Hide reply links' tokens as an agent sees the list, and mark those items (what chat cards are drawn from)")
	cmd.Flags().BoolVar(&suggest, "suggest", false, "Add TypeSafe Jev's suggestion to each item (computed and stored when missing; needs 'jev enable hil')")
	cmd.Flags().BoolVar(&resuggest, "resuggest", false, "Recompute and store every suggestion (implies --suggest)")
	return cmd
}

// hideReplyTokens hides, for a marked process, the reply tokens in items'
// data, as in the run's data (workflow.ReplyRedactor): a token there can
// answer a reply that opens once the item is approved.
func hideReplyTokens(ctx context.Context, db *sql.DB, items []hilItem) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ExecutionID
	}
	rr := workflow.NewReplyRedactor(ctx, db, ids...)
	for i := range items {
		it := &items[i]
		shown := func() string {
			b, _ := json.Marshal([]any{it.ReadonlyData, it.EditableData, it.Send})
			return string(b)
		}
		before := shown()
		it.ReadonlyData, it.EditableData = rr.Map(it.ReadonlyData), rr.Map(it.EditableData)
		if it.Send != nil {
			// The card repeats the row's data: hide the same there.
			var doc map[string]interface{}
			var send hilsend.Payload
			raw, _ := json.Marshal(it.Send)
			if json.Unmarshal(raw, &doc) == nil {
				raw, _ = json.Marshal(rr.Map(doc))
				if json.Unmarshal(raw, &send) == nil {
					it.Send = &send
				}
			}
		}
		it.Redacted = shown() != before
	}
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
			if done, err := d.decideIfBrowserSubmit(cmd, args[0], true); done {
				return err
			}
			if err := d.requireOperatorIfOutbound(cmd, args[0]); err != nil {
				return err
			}
			var check map[string]interface{}
			if err := json.Unmarshal([]byte(editedData), &check); err != nil {
				return fmt.Errorf("--data is not valid JSON: %w", err)
			}
			if err := workflow.CheckReplyEdit(editedData); err != nil {
				return d.InvalidInput("--data: %v", err)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			if err := d.decideItem(cmd, db.DB, args[0], `status='approved', edited_data=?`, editedData); err != nil {
				return err
			}
			_ = commsync.SetStatus(cmd.Context(), db.DB, args[0], commsync.Approved, commsync.Detail{Edited: check})
			return printDecision(cmd.OutOrStdout(), d.JSONOutput(), args[0], "approved")
		},
	}
	cmd.Flags().StringVar(&editedData, "data", "", "JSON object overriding the item's editable data")
	addIfDigest(cmd)
	return cmd
}

func (d Deps) newHILRejectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "reject <id>",
		Short:   "Reject a pending Human-in-Loop item (the workflow errors out)",
		Example: `  monoagentcli hil reject 1a2b3c4d`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			args[0] = d.fullHILID(args[0])
			if done, err := d.approveIfSend(cmd, args[0], false, ""); done {
				return err
			}
			if done, err := d.decideIfBrowserSubmit(cmd, args[0], false); done {
				return err
			}
			if err := d.requireOperatorIfOutbound(cmd, args[0]); err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			if err := d.decideItem(cmd, db.DB, args[0], `status='rejected'`); err != nil {
				return err
			}
			_ = commsync.SetStatus(cmd.Context(), db.DB, args[0], commsync.Rejected, commsync.Detail{})
			return printDecision(cmd.OutOrStdout(), d.JSONOutput(), args[0], "rejected")
		},
	}
	addIfDigest(cmd)
	return cmd
}

// noDaemonHint follows a decision when no daemon is running: the paused run
// only resumes in a running engine, and a `workflow run` stops waiting when
// its run pauses for a review.
const noDaemonHint = "No daemon is running, so the paused run stays WAITING until one is: start it with `monoagentcli daemon`."

// printDecision reports an approved or rejected item, with noDaemonHint
// when no daemon will resume its run (a "hint" field with --json).
func printDecision(w io.Writer, jsonOut bool, id, status string) error {
	_, live := daemonhb.Read()
	if jsonOut {
		rec := map[string]string{"id": id, "status": status}
		if !live {
			rec["hint"] = noDaemonHint
		}
		return json.NewEncoder(w).Encode(rec)
	}
	verb := "Approved"
	if status == "rejected" {
		verb = "Rejected"
	}
	fmt.Fprintf(w, "%s HIL item %s\n", verb, id)
	if !live {
		fmt.Fprintf(w, "Hint: %s\n", noDaemonHint)
	}
	return nil
}
