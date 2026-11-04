package workflowio

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Import statuses (`workflow import --json` "status").
const (
	ImportCreated   = "created"
	ImportUpdated   = "updated"
	ImportUnchanged = "unchanged"
)

// importIndexEntry remembers one import so a repeated import of the same
// file updates that workflow instead of creating a duplicate.
type importIndexEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"` // absolute --file path, or "stdin"
	Hash   string `json:"hash"`   // workflowContentHash at import time
	// ImportedAt is when the import was recorded (zero in entries written
	// before v0.75).
	ImportedAt time.Time `json:"importedAt,omitempty"`
}

func importIndexPath() string { return expandPath("~/.monoagent/workflow-imports.json") }

func loadImportIndex() []importIndexEntry {
	var idx []importIndexEntry
	if b, err := os.ReadFile(importIndexPath()); err == nil {
		_ = json.Unmarshal(b, &idx)
	}
	return idx
}

// recordImport upserts the entry for id (one entry per workflow).
func recordImport(e importIndexEntry) {
	if e.ImportedAt.IsZero() {
		e.ImportedAt = time.Now().UTC()
	}
	idx := loadImportIndex()
	out := idx[:0]
	for _, old := range idx {
		if old.ID != e.ID {
			out = append(out, old)
		}
	}
	out = append(out, e)
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	path := importIndexPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func workflowImportSource(inputFile string) string {
	if inputFile == "" {
		return "stdin"
	}
	if abs, err := filepath.Abs(inputFile); err == nil {
		return abs
	}
	return inputFile
}

// workflowContentHash hashes what a workflow does, independent of ids and
// timestamps (ids may have been remapped on a previous import): name,
// description, nodes (type, name, config, position, disabled) and the
// connections between them.
func workflowContentHash(wf *workflow.Workflow) string {
	type cnode struct {
		Type     string         `json:"t"`
		Name     string         `json:"n"`
		Config   map[string]any `json:"c,omitempty"`
		X        float64        `json:"x"`
		Y        float64        `json:"y"`
		Disabled bool           `json:"d,omitempty"`
	}
	keys := map[string]string{} // node id → canonical node JSON
	nodes := make([]string, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		cfg := n.Config
		if cfg == nil && n.ConfigRaw != "" {
			_ = json.Unmarshal([]byte(n.ConfigRaw), &cfg)
		}
		b, _ := json.Marshal(cnode{n.Type, n.Name, cfg, n.PositionX, n.PositionY, n.Disabled})
		keys[n.ID] = string(b)
		nodes = append(nodes, string(b))
	}
	sort.Strings(nodes)
	conns := make([]string, 0, len(wf.Connections))
	for _, c := range wf.Connections {
		conns = append(conns, strings.Join([]string{keys[c.SourceNodeID], c.SourceHandle, keys[c.TargetNodeID], c.TargetHandle}, "\x00"))
	}
	sort.Strings(conns)
	h := sha256.New()
	_ = json.NewEncoder(h).Encode([]any{wf.Name, wf.Description, nodes, conns})
	return hex.EncodeToString(h.Sum(nil))
}

// OwnedWorkflow returns the local workflow id if it exists and belongs to
// profileID, else nil. The ownership rule is workflow.OwnedBy: an empty
// profileID or a workflow with no profile owns nothing (CheckOwnedWorkflow
// says why).
func OwnedWorkflow(ctx context.Context, store *workflow.HybridWorkflowStore, db *sql.DB, profileID, id string) *workflow.Workflow {
	wf, _ := CheckOwnedWorkflow(ctx, store, db, profileID, id)
	return wf
}

// CheckOwnedWorkflow is OwnedWorkflow with the reason: (nil, nil) when the
// workflow is absent or belongs to another profile, workflow.ErrNoProfile
// for an empty profileID, and workflow.ErrLegacyWorkflowNoProfile for a
// workflow that carries no profile.
func CheckOwnedWorkflow(ctx context.Context, store *workflow.HybridWorkflowStore, db *sql.DB, profileID, id string) (*workflow.Workflow, error) {
	if profileID == "" {
		return nil, workflow.ErrNoProfile
	}
	if id == "" {
		return nil, nil
	}
	wf, err := store.GetWorkflow(ctx, id)
	if err != nil || wf == nil {
		return nil, err
	}
	owned, err := workflow.OwnedBy(wf, profileID)
	if err != nil || !owned {
		return nil, err
	}
	// The SQLite row, when there is one, must agree with the file.
	var owner string
	_ = db.QueryRowContext(ctx, `SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&owner)
	if owner != "" && owner != profileID {
		return nil, nil
	}
	return wf, nil
}

// importMatch is what findImportTarget decided about an import.
type importMatch struct {
	// Target is the local workflow the file corresponds to (nil: none).
	Target *workflow.Workflow
	// MayUpdate: Target is an unedited earlier import of this file, so a
	// changed file may replace it in place. Otherwise a differing file is
	// imported as a copy — never over the user's own work.
	MayUpdate bool
	// Clash names a local workflow with the same name that the import does
	// not replace (reported as a warning).
	Clash string
	// By says how Target was found: matchByID (the file's own id),
	// matchByImport (an earlier import of this file) or matchByContent (an
	// identical unindexed workflow).
	By string
}

const (
	matchByID      = "id"
	matchByImport  = "import"
	matchByContent = "content"
)

// findImportTarget finds the local workflow an import corresponds to: the
// file's own id, else a workflow imported earlier from the same source
// (same content, or same name), else — for workflows the index never saw —
// only a workflow with IDENTICAL content. Only an indexed earlier import
// that was not edited since may be updated in place.
func findImportTarget(ctx context.Context, store *workflow.HybridWorkflowStore, db *sql.DB, profileID string,
	wf *workflow.Workflow, source, hash string) importMatch {
	idx := loadImportIndex()
	entryFor := func(id string) *importIndexEntry {
		for i := len(idx) - 1; i >= 0; i-- {
			if idx[i].ID == id {
				return &idx[i]
			}
		}
		return nil
	}
	decide := func(existing *workflow.Workflow, by string) importMatch {
		return importMatch{Target: existing, MayUpdate: uneditedSinceImport(existing, entryFor(existing.ID)), By: by}
	}
	// An identical copy made by an earlier import of this file: once the
	// original was edited, re-imports land there ("unchanged") instead of
	// making one more copy each time.
	identicalCopy := func() *workflow.Workflow {
		for i := len(idx) - 1; i >= 0; i-- {
			if e := idx[i]; e.Source == source && e.Hash == hash {
				if c := OwnedWorkflow(ctx, store, db, profileID, e.ID); c != nil && workflowContentHash(c) == hash {
					return c
				}
			}
		}
		return nil
	}
	if existing := OwnedWorkflow(ctx, store, db, profileID, wf.ID); existing != nil {
		m := decide(existing, matchByID)
		if !m.MayUpdate && workflowContentHash(existing) != hash {
			if c := identicalCopy(); c != nil {
				return importMatch{Target: c, By: matchByImport}
			}
		}
		return m
	}
	for _, match := range []func(importIndexEntry) bool{
		func(e importIndexEntry) bool { return e.Source == source && e.Hash == hash },
		func(e importIndexEntry) bool { return e.Source == source && e.Name == wf.Name },
	} {
		for i := len(idx) - 1; i >= 0; i-- {
			if match(idx[i]) {
				if existing := OwnedWorkflow(ctx, store, db, profileID, idx[i].ID); existing != nil {
					return decide(existing, matchByImport)
				}
			}
		}
	}
	return matchUnindexed(ctx, store, db, profileID, wf, hash)
}

// uneditedSinceImport reports whether w is still exactly what the recorded
// import e wrote: same content hash, and not saved after e was recorded.
func uneditedSinceImport(w *workflow.Workflow, e *importIndexEntry) bool {
	if w == nil || e == nil || e.Hash == "" || workflowContentHash(w) != e.Hash {
		return false
	}
	return e.ImportedAt.IsZero() || !w.UpdatedAt.After(e.ImportedAt.Add(2*time.Second))
}

// matchUnindexed looks at local workflows the import index does not know
// (e.g. imported by v0.70, or created by hand) with the same name. Only an
// IDENTICAL one (ids and timestamps ignored) is a match — reported as
// unchanged. A same-named workflow with different content is never
// touched; it is returned as a clash so the import says it made a copy.
func matchUnindexed(ctx context.Context, store *workflow.HybridWorkflowStore, db *sql.DB, profileID string,
	wf *workflow.Workflow, hash string) importMatch {
	list, err := store.ListWorkflows(ctx, profileID)
	if err != nil {
		return importMatch{}
	}
	clash := ""
	for _, cand := range list {
		if cand.Name != wf.Name {
			continue
		}
		full := OwnedWorkflow(ctx, store, db, profileID, cand.ID)
		if full == nil {
			continue
		}
		if workflowContentHash(full) == hash {
			return importMatch{Target: full, By: matchByContent}
		}
		if clash == "" {
			clash = full.ID
		}
	}
	return importMatch{Clash: clash}
}

// importCopy says which existing workflow an import was copied beside and
// why (`workflow import --json` "copyOf"/"copyReason"); zero when the
// import did not make a copy.
type importCopy struct {
	Of     string // existing workflow id
	Reason string // copyReasonID | copyReasonName | copyReasonImport
}

const (
	copyReasonID     = "id"     // the file's own id is a locally edited workflow
	copyReasonName   = "name"   // a different workflow has the same name
	copyReasonImport = "import" // the earlier import of this file was edited
)

// copyReasonFor maps how a match was found to its copyReason.
func copyReasonFor(by string) string {
	switch by {
	case matchByID:
		return copyReasonID
	case matchByImport:
		return copyReasonImport
	}
	return copyReasonName
}

// copyWarning is the warning of an import that did not replace id; by
// says why id matched (matchByID, matchByImport, or "" for a name clash).
func copyWarning(id, by string) string {
	var why string
	switch by {
	case matchByID:
		why = "a workflow with this id already exists and was edited locally"
	case matchByImport:
		why = "the workflow imported earlier from this file was edited locally"
	default:
		why = "a workflow with this name already exists"
	}
	return fmt.Sprintf("%s: %s; imported as a copy — use --replace %s to replace it", why, id, id)
}

// missingBundleHint lists bundled packages that were not installed and the
// command that installs them (a re-import with --yes; imports are
// idempotent, so the workflow itself is left unchanged).
func missingBundleHint(items []workflowbundle.ImportItem, inputFile string) ([]string, string) {
	var missing []string
	for _, it := range items {
		// A package the exporter could not bundle cannot be installed by
		// re-importing; printBundleImport says what to do instead.
		if it.Status == "missing" && !it.NotBundled {
			missing = append(missing, it.ID)
		}
	}
	if len(missing) == 0 {
		return nil, ""
	}
	if inputFile == "" {
		return missing, "monoagentcli workflow import --yes < <workflow file>"
	}
	return missing, fmt.Sprintf("monoagentcli workflow import --file %s --yes", shellQuoteArg(workflowImportSource(inputFile)))
}

func shellQuoteArg(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// printWorkflowImport handles the bundled automations and prints the
// import result (JSON or human), including the status and, when bundled
// packages are missing, which ones and the exact command installing them.
func (d Deps) printWorkflowImport(cmd *cobra.Command, wf *workflow.Workflow, status string, warnings []string,
	remapped, remappedConns map[string]string, raw []byte, yes, replaceAutomations bool, inputFile string, copied importCopy, note string) error {
	// Bundled automations (workflow export --bundle-automations):
	// report present/missing ones, install missing with --yes or
	// after a prompt (only when stdin is free, i.e. --file).
	bundled := d.Bundle.HandleBundledAutomations(raw, workflowbundle.ImportOptions{
		Yes:         yes,
		Replace:     replaceAutomations,
		Interactive: !d.JSONOutput() && inputFile != "" && d.StdinIsTerminal(),
		In:          cmd.InOrStdin(),
		Out:         os.Stderr,
	})
	missing, installCmd := missingBundleHint(bundled, inputFile)

	if note != "" {
		fmt.Fprintln(os.Stderr, "Note: "+note)
	}
	if d.JSONOutput() {
		out := map[string]interface{}{"id": wf.ID, "name": wf.Name, "status": status}
		if note != "" {
			out["approved"], out["approval_note"] = false, note
		}
		if len(warnings) > 0 {
			out["warnings"] = warnings
		}
		if copied.Of != "" {
			out["copyOf"], out["copyReason"] = copied.Of, copied.Reason
		}
		if bundled != nil {
			out["automations"] = bundled
		}
		if len(missing) > 0 {
			out["missingAutomations"] = missing
			out["installCommand"] = installCmd
		}
		if len(remapped) > 0 {
			out["remapped_node_ids"] = remapped
		}
		if len(remappedConns) > 0 {
			out["remapped_connection_ids"] = remappedConns
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	switch status {
	case ImportUnchanged:
		fmt.Fprintf(cmd.OutOrStdout(), "Workflow %q is already imported and unchanged (id: %s)\n", wf.Name, wf.ID)
	case ImportUpdated:
		fmt.Fprintf(cmd.OutOrStdout(), "Updated workflow %q in place (id: %s, %d nodes, %d connections)\n",
			wf.Name, wf.ID, len(wf.Nodes), len(wf.Connections))
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "Imported workflow %q as id: %s  (%d nodes, %d connections)\n",
			wf.Name, wf.ID, len(wf.Nodes), len(wf.Connections))
	}
	printRemapped := func(label string, m map[string]string) {
		if len(m) == 0 {
			return
		}
		parts := make([]string, 0, len(m))
		for old, newID := range m {
			parts = append(parts, old+" → "+newID)
		}
		sort.Strings(parts)
		fmt.Fprintf(cmd.OutOrStdout(), "Remapped %s (already used by another workflow): %s\n", label, strings.Join(parts, ", "))
	}
	for _, w := range warnings {
		fmt.Fprintln(cmd.OutOrStdout(), "Warning:", w)
	}
	printRemapped("node ids", remapped)
	printRemapped("connection ids", remappedConns)
	workflowbundle.PrintBundleImport(cmd.OutOrStdout(), bundled)
	if len(missing) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "The workflow was imported, but it needs %d automation(s) that are not installed: %s\nInstall them with: %s\n",
			len(missing), strings.Join(missing, ", "), installCmd)
	}
	return nil
}
