package workflowio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// ParseWorkflowDefinition parses workflow definition JSON for `workflow
// import`: the documented WorkflowFile shape natively, plus legacy exports
// (a marshaled *Workflow whose nodes use "node_type"/"position_x" and whose
// connections use "source_node_id"/"target_node_id") converted first.
// Without the conversion, legacy JSON silently parses to nodes with an
// empty type and connections referencing nothing. Any node that still has
// no type afterwards is rejected — a workflow like that must never be
// persisted.
func (d Deps) ParseWorkflowDefinition(raw []byte) (workflow.Workflow, error) {
	normalized, err := NormalizeLegacyWorkflowJSON(raw)
	if err != nil {
		return workflow.Workflow{}, d.InvalidInput("parse workflow JSON: %v", err)
	}
	wf, err := workflow.ParseWorkflowFileBytes(normalized)
	if err != nil {
		return workflow.Workflow{}, d.InvalidInput("parse workflow JSON: %v", err)
	}
	for _, n := range wf.Nodes {
		if n.Type == "" {
			id := n.ID
			if id == "" {
				id = n.Name
			}
			return workflow.Workflow{}, d.InvalidInput(
				"node %q is missing a type — current files use \"type\", legacy exports \"node_type\"", id)
		}
	}
	return wf, nil
}

// NormalizeLegacyWorkflowJSON rewrites legacy export JSON — a marshaled
// *workflow.Workflow — into the documented WorkflowFile shape: node
// "node_type" → "type", "position_x"/"position_y" → "position": {"x","y"},
// connection "source_node_id"/"target_node_id" → "source"/"target". Mixed
// documents are fine: only legacy keys are rewritten, and only when their
// native counterpart is absent. Input without any legacy key is returned
// unchanged.
func NormalizeLegacyWorkflowJSON(raw []byte) ([]byte, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	legacy := false
	if nodes, ok := doc["nodes"].([]interface{}); ok {
		for _, nm := range nodes {
			n, ok := nm.(map[string]interface{})
			if !ok {
				continue
			}
			if nt, ok := n["node_type"]; ok {
				legacy = true
				if _, has := n["type"]; !has {
					n["type"] = nt
				}
				delete(n, "node_type")
			}
			if px, ok := n["position_x"]; ok {
				legacy = true
				pos := map[string]interface{}{"x": px}
				if py, ok := n["position_y"]; ok {
					pos["y"] = py
					delete(n, "position_y")
				}
				if _, has := n["position"]; !has {
					n["position"] = pos
				}
				delete(n, "position_x")
			}
		}
	}
	if conns, ok := doc["connections"].([]interface{}); ok {
		for _, cm := range conns {
			c, ok := cm.(map[string]interface{})
			if !ok {
				continue
			}
			if s, ok := c["source_node_id"]; ok {
				legacy = true
				if _, has := c["source"]; !has {
					c["source"] = s
				}
				delete(c, "source_node_id")
			}
			if tg, ok := c["target_node_id"]; ok {
				legacy = true
				if _, has := c["target"]; !has {
					c["target"] = tg
				}
				delete(c, "target_node_id")
			}
		}
	}
	if !legacy {
		return raw, nil
	}
	return json.Marshal(doc)
}

// newWorkflowImportCmd imports a full workflow definition from a JSON file.
func (d Deps) newImportCmd() *cobra.Command {
	var inputFile string
	var overwrite, yes, asNew bool
	var replaceID string
	var replaceAutomations bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a workflow from a JSON file (--file or stdin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw []byte
			var err error
			if inputFile != "" {
				raw, err = os.ReadFile(inputFile)
			} else {
				raw, err = io.ReadAll(cmd.InOrStdin())
			}
			if err != nil {
				if os.IsNotExist(err) {
					return d.NotFound("read input: %v", err)
				}
				return fmt.Errorf("read input: %w", err)
			}

			// Accepts the documented WorkflowFile shape natively and legacy
			// exports (marshaled *Workflow JSON) via key conversion; rejects
			// any node that ends up without a type.
			wf, err := d.ParseWorkflowDefinition(raw)
			if err != nil {
				return err
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			now := time.Now().UTC()

			// Idempotent, never destructive: an identical local workflow is
			// "unchanged"; only an unedited earlier import of this file (or
			// --replace <id>) is updated in place; any other same-named
			// workflow is left alone and the file is imported as a copy.
			// --as-new always copies.
			source, hash := workflowImportSource(inputFile), workflowContentHash(&wf)
			status := ImportCreated
			var match importMatch
			var warnings []string
			var copied importCopy
			switch {
			case replaceID != "":
				t := OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), replaceID)
				if t == nil {
					return d.NotFound("--replace: no workflow %q in this profile", replaceID)
				}
				match = importMatch{Target: t, MayUpdate: true, By: matchByID}
			case !asNew:
				match = findImportTarget(ctx, store, db.DB, d.ProfileID(), &wf, source, hash)
			}
			target := match.Target
			if match.Clash != "" {
				warnings = append(warnings, copyWarning(match.Clash, ""))
				copied = importCopy{Of: match.Clash, Reason: copyReasonName}
			}
			switch {
			case target != nil && workflowContentHash(target) == hash:
				status = ImportUnchanged
				wf.ID = target.ID
			case target != nil && match.MayUpdate:
				status, overwrite = ImportUpdated, true
				wf.ID = target.ID
				if !target.CreatedAt.IsZero() {
					now = target.CreatedAt
				}
			case target != nil && overwrite && match.By == matchByID:
				// --overwrite keeps the file's id, but that id is a
				// different, locally edited workflow: never replace it
				// implicitly, and never quietly import a copy instead.
				return d.InvalidInput("--overwrite: workflow %s already exists here and was edited locally; use --replace %s to replace it, or import without --overwrite to get a copy", target.ID, target.ID)
			case target != nil:
				// Edited (or created) locally since: keep the user's work.
				warnings = append(warnings, copyWarning(target.ID, match.By))
				copied = importCopy{Of: target.ID, Reason: copyReasonFor(match.By)}
				wf.ID, overwrite = uuid.New().String(), false
			case asNew || wf.ID == "":
				wf.ID = uuid.New().String()
			case !workflowIDTaken(ctx, store, wf.ID):
				// The id is free here: keep it, so a re-import finds it.
			case overwrite:
				return d.InvalidInput("--overwrite: id %s is used by a workflow in another profile; import without --overwrite to get a copy with a new id", wf.ID)
			default:
				wf.ID = uuid.New().String() // taken by another profile's workflow
			}
			wf.CreatedAt = now
			wf.UpdatedAt = time.Now().UTC()
			if status == ImportUnchanged {
				recordImport(importIndexEntry{ID: wf.ID, Name: wf.Name, Source: source, Hash: hash})
				return d.printWorkflowImport(cmd, &wf, status, warnings, nil, nil, raw, yes, replaceAutomations, inputFile, importCopy{})
			}

			// workflow_nodes.id and workflow_connections.id are globally
			// unique (PRIMARY KEY) across ALL workflows, so importing a file
			// whose ids are already used by another workflow fails with
			// "UNIQUE constraint failed" (bundled examples all use "trigger"
			// and "trigger-to-…"). Remap each colliding id deterministically
			// across nodes AND connections (including edge from/to refs).
			// Node ids referenced inside node configs are rare and
			// intentionally left untouched.
			taken := map[string]bool{}
			takenConns := map[string]bool{}
			for _, q := range []struct{ label, sql string }{
				{"node", `SELECT id FROM workflow_nodes WHERE workflow_id != ?`},
				{"connection", `SELECT id FROM workflow_connections WHERE workflow_id != ?`},
			} {
				rows, qerr := db.DB.QueryContext(ctx, q.sql, wf.ID)
				if qerr != nil {
					return fmt.Errorf("query existing %s ids: %w", q.label, qerr)
				}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						rows.Close()
						return fmt.Errorf("scan existing %s id: %w", q.label, err)
					}
					if q.label == "node" {
						taken[id] = true
					} else {
						takenConns[id] = true
					}
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					return fmt.Errorf("iterate existing %s ids: %w", q.label, err)
				}
				rows.Close()
			}

			// freeID returns id unchanged (reserving it) or the first free
			// deterministic variant "<id>-2", "<id>-3", … .
			freeID := func(used map[string]bool, id string) string {
				if !used[id] {
					used[id] = true
					return id
				}
				newID := fmt.Sprintf("%s-2", id)
				for n := 2; used[newID]; n++ {
					newID = fmt.Sprintf("%s-%d", id, n)
				}
				used[newID] = true
				return newID
			}

			remapped := map[string]string{}
			for i := range wf.Nodes {
				if wf.Nodes[i].ID == "" {
					continue // gets a fresh UUID below
				}
				newID := freeID(taken, wf.Nodes[i].ID)
				if newID != wf.Nodes[i].ID {
					remapped[wf.Nodes[i].ID] = newID
					wf.Nodes[i].ID = newID
				}
			}
			remappedConns := map[string]string{}
			for i := range wf.Connections {
				if newID, ok := remapped[wf.Connections[i].SourceNodeID]; ok {
					wf.Connections[i].SourceNodeID = newID
				}
				if newID, ok := remapped[wf.Connections[i].TargetNodeID]; ok {
					wf.Connections[i].TargetNodeID = newID
				}
				if wf.Connections[i].ID == "" {
					continue // gets a fresh UUID below
				}
				newID := freeID(takenConns, wf.Connections[i].ID)
				if newID != wf.Connections[i].ID {
					remappedConns[wf.Connections[i].ID] = newID
					wf.Connections[i].ID = newID
				}
			}

			// An import lands in the profile it was run for. The file's own
			// profile_id is where it was exported from (or nothing, which
			// the store reads as default), never where it is going.
			wf.ProfileID = d.ProfileID()

			// Finalize every id BEFORE any store call: assign fresh UUIDs to
			// nodes/connections that arrived without one. This must happen
			// here, not later (the old code assigned node UUIDs only right
			// before SaveWorkflowNodes), so the validation below — and the
			// atomic persist after it — see the exact ids that will
			// actually be written.
			for i := range wf.Nodes {
				wf.Nodes[i].WorkflowID = wf.ID
				if wf.Nodes[i].ID == "" {
					wf.Nodes[i].ID = uuid.New().String()
				}
			}
			for i := range wf.Connections {
				wf.Connections[i].WorkflowID = wf.ID
				if wf.Connections[i].ID == "" {
					wf.Connections[i].ID = uuid.New().String()
				}
			}

			// Validate structure before writing anything at all: a
			// connection referencing a node id that doesn't exist in this
			// workflow (a stale id, a typo in a hand-edited or generated
			// file) fails here, with nothing touched — not partway through
			// persistence, and not as a raw SQL foreign-key error. This
			// matters especially for --overwrite: catching it here means an
			// invalid replacement never disturbs the existing workflow at
			// all, rather than needing to be rolled back after the fact.
			if err := workflow.ValidateConnectionsAgainstNodes(wf.Nodes, wf.Connections); err != nil {
				return err
			}
			// The rest of `workflow validate` only warns: the file is
			// imported to be fixed here. --from n8n lists the same errors
			// in its report.
			if from := cmd.Flags().Lookup("from"); from == nil || from.Value.String() == "" {
				for _, e := range workflow.ValidateWorkflow(&wf, workflow.ValidateOptions{}).Errors {
					warnings = append(warnings, "fails `workflow validate`: "+e)
				}
			}

			if err := d.CreateWorkflow(operator.ApprovingContext(ctx), store, &wf, overwrite); err != nil {
				return err
			}

			recordImport(importIndexEntry{ID: wf.ID, Name: wf.Name, Source: source, Hash: hash})
			return d.printWorkflowImport(cmd, &wf, status, warnings, remapped, remappedConns, raw, yes, replaceAutomations, inputFile, copied)
		},
	}

	cmd.Flags().StringVarP(&inputFile, "file", "f", "", "Path to JSON file (default: stdin)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Keep the id from the file; refused when that id is a different, locally edited workflow (use --replace <id>) or belongs to another profile")
	cmd.Flags().StringVar(&replaceID, "replace", "", "Replace this existing workflow (by id) with the file's content")
	cmd.Flags().BoolVar(&asNew, "as-new", false, "Always create a new workflow, even when this file was imported before")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Install automations bundled in the file that are not installed yet")
	cmd.Flags().BoolVar(&replaceAutomations, "replace-automations", false,
		"Replace installed automations whose bundled copy (same version) has different content, after showing the review (asks unless --yes)")
	// With --json a failure is also {"error","code"} on stdout (contracts
	// §5), as the automation commands do; the exit code is unchanged.
	d.WithJSONErrors(cmd)
	d.withN8nImport(cmd)
	return cmd
}

// WorkflowFileFromWorkflow converts a stored *Workflow into the documented
// WorkflowFile JSON shape — the same shape `workflow import` (and the file
// store) parses natively — so export→import roundtrips are lossless. Export
// used to emit a raw *Workflow marshal ("node_type"/"source_node_id" keys),
// which import no longer produces natively; that legacy shape is still
// accepted on import.
func WorkflowFileFromWorkflow(wf *workflow.Workflow) workflow.WorkflowFile {
	file := workflow.WorkflowFile{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		Version:     wf.Version,
		IsActive:    wf.IsActive,
		ProfileID:   wf.ProfileID,
		CreatedAt:   wf.CreatedAt,
		UpdatedAt:   wf.UpdatedAt,
	}
	for _, n := range wf.Nodes {
		fn := workflow.WorkflowFileNode{
			ID:       n.ID,
			Type:     n.Type,
			Name:     n.Name,
			Disabled: n.Disabled,
			Config:   n.Config,
			Schema:   n.Schema,
		}
		fn.Position.X = n.PositionX
		fn.Position.Y = n.PositionY
		if fn.Config == nil {
			fn.Config = map[string]interface{}{}
		}
		file.Nodes = append(file.Nodes, fn)
	}
	for _, c := range wf.Connections {
		file.Connections = append(file.Connections, workflow.WorkflowFileEdge{
			ID:           c.ID,
			Source:       c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			Target:       c.TargetNodeID,
			TargetHandle: c.TargetHandle,
		})
	}
	return file
}

// newWorkflowExportCmd exports a workflow as JSON.
func (d Deps) newExportCmd() *cobra.Command {
	var outputFile string
	var bundle, useSuggested bool
	var autoDomains []string

	cmd := &cobra.Command{
		Use:   "export <id>",
		Short: "Export a workflow as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, args[0])
			if err != nil {
				return fmt.Errorf("get workflow: %w", err)
			}
			if wf == nil || OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), args[0]) == nil {
				return d.NotFound("workflow %q not found", args[0])
			}

			var wfFile interface{} = WorkflowFileFromWorkflow(wf)
			if bundle {
				domains, err := d.Bundle.ParseAutomationDomains(autoDomains)
				if err != nil {
					return err
				}
				b, err := d.Bundle.BundleWorkflowAutomations(WorkflowFileFromWorkflow(wf),
					workflowbundle.Options{Domains: domains, UseSuggested: useSuggested})
				if err != nil {
					return err
				}
				for _, w := range append(workflowbundle.SuggestedNotices(b), workflowbundle.UnbundledWarnings(b)...) {
					fmt.Fprintln(os.Stderr, w)
				}
				wfFile = b
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if outputFile != "" {
				// Encode fully, then write via temp file + rename: a failed
				// export never leaves an empty or truncated file behind.
				b, err := json.MarshalIndent(wfFile, "", "  ")
				if err != nil {
					return err
				}
				if err := workflowbundle.WriteFileAtomic(outputFile, append(b, '\n')); err != nil {
					return fmt.Errorf("write output file: %w", err)
				}
				fmt.Fprintf(os.Stdout, "Exported workflow %q to %s\n", wf.Name, outputFile)
				return nil
			}
			return enc.Encode(wfFile)
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write to file instead of stdout")
	cmd.Flags().BoolVar(&bundle, "bundle-automations", false, "Embed the automation packages the workflow's nodes use")
	cmd.Flags().StringArrayVar(&autoDomains, "automation-domains", nil,
		"With --bundle-automations: site domains for a bundled package's exported copy, as <id>=<site,...> (repeatable)")
	cmd.Flags().BoolVar(&useSuggested, "use-suggested-domains", false,
		"With --bundle-automations: give legacy packages the domains suggested from their navigate URLs")
	return cmd
}

// workflowIDTaken reports whether any profile already has a workflow with id.
func workflowIDTaken(ctx context.Context, store *workflow.HybridWorkflowStore, id string) bool {
	wf, err := store.GetWorkflow(ctx, id)
	// A workflow that cannot be read is not free either: do not reuse its id.
	return err != nil || wf != nil
}
