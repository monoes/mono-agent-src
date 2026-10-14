package workflowtemplates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// newWorkflowTemplatesCmd manages bundled, ready-to-use workflow templates
// (e.g. "Outlook Email Sync") that ship with the CLI/app and can be turned
// into a real, editable workflow with one command.
func (d Deps) newTemplatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: i18n.T("workflow.templates.short"),
	}
	cmd.AddCommand(
		d.newTemplatesListCmd(),
		d.newTemplatesShowCmd(),
		d.newTemplatesUseCmd(),
		d.RunCmd(),
	)
	return cmd
}

// newWorkflowSearchCmd searches bundled templates AND the user's saved
// workflows in one place. This is the "what can this thing already do for me?"
// entry point — an agent that knows nothing but the binary name can run it,
// find something relevant, and get the exact command to run it.
func (d Deps) newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search [query]",
		Short: i18n.T("workflow.search.short"),
		Long:  i18n.T("workflow.search.long"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = strings.ToLower(args[0])
			}
			matches := func(fields ...string) bool {
				if query == "" {
					return true
				}
				for _, f := range fields {
					if strings.Contains(strings.ToLower(f), query) {
						return true
					}
				}
				return false
			}

			type hit struct {
				Source  string   `json:"source"` // "template" or "workflow"
				ID      string   `json:"id"`
				Name    string   `json:"name"`
				Inputs  []string `json:"inputs,omitempty"`
				Active  bool     `json:"active,omitempty"`
				Command string   `json:"command"`
			}
			var hits []hit

			for _, t := range workflow.ListTemplates() {
				if !matches(t.ID, t.Name, t.Description) {
					continue
				}
				runCmd := "monoagentcli workflow templates run " + t.ID
				if ex := ExampleInputJSON(t.Inputs); ex != "" {
					runCmd += " --input '" + ex + "'"
				}
				hits = append(hits, hit{Source: "template", ID: t.ID, Name: t.Name, Inputs: t.Inputs, Command: runCmd})
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			ctx := context.Background()
			store := d.WorkflowStore(db)
			saved, err := store.ListWorkflows(ctx, d.ProfileID())
			if err != nil {
				return fmt.Errorf("list workflows: %w", err)
			}
			for _, wf := range saved {
				nodeTypes := make([]string, 0, len(wf.Nodes))
				for _, n := range wf.Nodes {
					nodeTypes = append(nodeTypes, n.Type)
				}
				if !matches(append(nodeTypes, wf.ID, wf.Name, wf.Description)...) {
					continue
				}
				hits = append(hits, hit{
					Source:  "workflow",
					ID:      wf.ID,
					Name:    wf.Name,
					Active:  wf.IsActive,
					Command: "monoagentcli workflow run " + wf.ID,
				})
			}

			if d.JSONOutput() {
				if hits == nil {
					hits = []hit{}
				}
				return json.NewEncoder(os.Stdout).Encode(hits)
			}

			if len(hits) == 0 {
				fmt.Fprintf(os.Stdout, "Nothing matches %q.\n", query)
				fmt.Fprintln(os.Stdout, "Try `monoagentcli workflow search` with no query, or `monoagentcli ref templates`.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SOURCE\tNAME\tRUN WITH")
			for _, h := range hits {
				fmt.Fprintf(w, "%s\t%s\t%s\n", h.Source, h.Name, h.Command)
			}
			return w.Flush()
		},
	}
}

// InstantiateTemplate turns a bundled template into a concrete, unsaved
// Workflow owned by profileID. Node IDs in the template are stable,
// human-readable placeholders (e.g. "trigger") shared across every
// instantiation, so they are remapped to fresh UUIDs — that is what lets the
// same template be instantiated (and run) many times concurrently without the
// copies colliding.
func InstantiateTemplate(tmplFile workflow.WorkflowFile, profileID string) workflow.Workflow {
	idMap := make(map[string]string, len(tmplFile.Nodes))
	for _, n := range tmplFile.Nodes {
		idMap[n.ID] = uuid.New().String()
	}

	now := time.Now().UTC()
	wf := workflow.Workflow{
		ID:          uuid.New().String(),
		Name:        tmplFile.Name,
		Description: tmplFile.Description,
		IsActive:    false,
		Version:     1,
		ProfileID:   profileID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	for _, fn := range tmplFile.Nodes {
		config := fn.Config
		if config == nil {
			config = map[string]interface{}{}
		}
		// people.sync_outlook_message scopes synced people/messages by
		// its own "profile_id" config field, independent of the
		// workflow's ProfileID — default it to this profile so the
		// template works correctly out of the box for non-default profiles.
		if fn.Type == "people.sync_outlook_message" {
			config["profile_id"] = profileID
		}
		wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{
			ID:         idMap[fn.ID],
			WorkflowID: wf.ID,
			Type:       fn.Type,
			Name:       fn.Name,
			PositionX:  fn.Position.X,
			PositionY:  fn.Position.Y,
			Disabled:   fn.Disabled,
			Config:     config,
		})
	}
	for _, fe := range tmplFile.Connections {
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{
			ID:           uuid.New().String(),
			WorkflowID:   wf.ID,
			SourceNodeID: idMap[fe.Source],
			SourceHandle: fe.SourceHandle,
			TargetNodeID: idMap[fe.Target],
			TargetHandle: fe.TargetHandle,
		})
	}
	return wf
}

// InputKeyList renders a template's trigger-data keys for table output.
func InputKeyList(inputs []string) string {
	if len(inputs) == 0 {
		return "(none)"
	}
	return strings.Join(inputs, ",")
}

// ExampleInputJSON builds a copy-pasteable --input value for a template, so an
// agent can run it without reading the node configs first.
func ExampleInputJSON(inputs []string) string {
	if len(inputs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(inputs))
	for _, key := range inputs {
		// Plural keys are arrays in the bundled templates (prompts, urls, …);
		// find_jobs' keywords is one search string.
		if strings.HasSuffix(key, "s") && key != "keywords" {
			parts = append(parts, fmt.Sprintf("%q:[\"...\",\"...\"]", key))
		} else {
			parts = append(parts, fmt.Sprintf("%q:\"...\"", key))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// newWorkflowTemplatesShowCmd prints everything needed to run one template.
func (d Deps) newTemplatesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <template-id>",
		Short: "Show a template's description, inputs, nodes, and the exact command to run it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmplFile, ok := workflow.GetTemplate(args[0])
			if !ok {
				return fmt.Errorf("unknown template %q. Run `monoagentcli workflow templates list` to see available IDs", args[0])
			}
			var meta workflow.Template
			for _, t := range workflow.ListTemplates() {
				if t.ID == args[0] {
					meta = t
					break
				}
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"id":          meta.ID,
					"name":        tmplFile.Name,
					"description": tmplFile.Description,
					"inputs":      meta.Inputs,
					"definition":  tmplFile,
				})
			}

			fmt.Fprintf(os.Stdout, "%s  (%s)\n\n%s\n\n", tmplFile.Name, args[0], tmplFile.Description)
			fmt.Fprintf(os.Stdout, "INPUT KEYS: %s\n\n", InputKeyList(meta.Inputs))

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NODE\tTYPE")
			for _, n := range tmplFile.Nodes {
				fmt.Fprintf(w, "%s\t%s\n", n.Name, n.Type)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "\nRun it:\n  monoagentcli workflow templates run %s", args[0])
			if ex := ExampleInputJSON(meta.Inputs); ex != "" {
				fmt.Fprintf(os.Stdout, " --input '%s'", ex)
			}
			fmt.Fprintln(os.Stdout)
			fmt.Fprintf(os.Stdout, "\nSave an editable copy instead:\n  monoagentcli workflow templates use %s\n", args[0])
			fmt.Fprintln(os.Stdout, "\nNode config details:  monoagentcli ref node <type>")
			return nil
		},
	}
}

func (d Deps) newTemplatesListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List bundled workflow templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			templates := workflow.ListTemplates()
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(templates)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tINPUT KEYS\tNAME\tDESCRIPTION")
			for _, t := range templates {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.ID, InputKeyList(t.Inputs), t.Name, t.Description)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "\nRun one:     monoagentcli workflow templates run <id> --input '{\"<key>\":\"...\"}'")
			fmt.Fprintln(os.Stdout, "Inspect one: monoagentcli workflow templates show <id>")
			return nil
		},
	}
	return cmd
}

func (d Deps) newTemplatesUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use <template-id>",
		Short: "Instantiate a bundled template as a new, editable workflow",
		Long: "Creates a new workflow for the current profile from a bundled template " +
			"(see `workflow templates list` for IDs). The workflow starts inactive — " +
			"fill in any required credentials (e.g. the Outlook account) via `workflow node set` " +
			"or the app, then `workflow activate` it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmplFile, ok := workflow.GetTemplate(args[0])
			if !ok {
				return fmt.Errorf("unknown template %q. Run `monoagentcli workflow templates list` to see available IDs", args[0])
			}
			if err := d.CheckNodeTypes(args[0], tmplFile); err != nil {
				return err
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			wf := InstantiateTemplate(tmplFile, d.ProfileID())
			for i := range wf.Nodes {
				wf.Nodes[i].WorkflowID = wf.ID
			}
			for i := range wf.Connections {
				wf.Connections[i].WorkflowID = wf.ID
			}

			// Defense-in-depth: a bundled template shouldn't ever have a
			// dangling connection, but validate the same way `workflow
			// import` does rather than trusting that invariant silently —
			// this is a fresh id either way (no --overwrite here), so a
			// caught error here still means nothing was written.
			if err := workflow.ValidateConnectionsAgainstNodes(wf.Nodes, wf.Connections); err != nil {
				return err
			}

			store := d.WorkflowStore(db)
			ctx := context.Background()
			if err := d.CreateWorkflow(ctx, store, &wf, false); err != nil {
				return err
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(wf)
			}
			fmt.Fprintf(os.Stdout, "Created workflow %q from template %q  (id: %s, %d nodes, %d connections)\n",
				wf.Name, args[0], wf.ID, len(wf.Nodes), len(wf.Connections))
			fmt.Fprintln(os.Stdout, "It starts inactive — set any required credentials, then `monoagentcli workflow activate "+wf.ID+"`.")
			return nil
		},
	}
	return cmd
}
