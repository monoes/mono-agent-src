package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"
)

// dedupeOrgGrants runs inside migration 173's transaction, before its unique
// indexes: nothing kept one live org_grants row per (profile, org, role,
// alias), or one org-tool row per (profile, org, role). It leaves one live row
// per slot and never gives a role anything it could not already use:
//
//   - Automation grants of one alias: the newest row stays (the one grant mode
//     runs today and counts calls against), tightened to the strictest of the
//     duplicates: approval required if any asks for it, the lowest call and
//     output limits, the highest tier, and a workflow pin another duplicate has
//     for the same workflow when it has none.
//   - Org-tool rows: the oldest row stays (the one grant mode reads), holding
//     only the tools every duplicate names, over only the orgs every duplicate
//     lists. A tool or org the duplicates disagree on is dropped (the operator
//     grants it again); with nothing left in common, or a row that cannot be
//     read, all of them are revoked.
//
// No row is deleted: the others are revoked in place, and every row revoked or
// rewritten is first copied, as it was, to org_grant_dedupe_log.
func dedupeOrgGrants(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS org_grant_dedupe_log (
		grant_id       TEXT NOT NULL,
		profile_id     TEXT NOT NULL,
		org_name       TEXT NOT NULL,
		role_id        TEXT NOT NULL,
		action         TEXT NOT NULL, -- revoked | rewritten
		kept_grant_id  TEXT,          -- the row left live; NULL when none was
		tools_json     TEXT NOT NULL, -- this and the next three: the row before
		org_tools_json TEXT NOT NULL,
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL,
		recorded_at    TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating org_grant_dedupe_log: %w", err)
	}
	groups, err := duplicateOrgGrants(ctx, tx)
	if err != nil {
		return err
	}
	w := grantRewriter{ctx: ctx, tx: tx, now: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, g := range groups {
		if g[0].orgTools {
			err = w.mergeOrgToolRows(g)
		} else {
			err = w.mergeAutomationRows(g)
		}
		if err != nil {
			return err
		}
	}
	if w.revoked > 0 {
		log.Printf("storage: org grants: revoked %d duplicate grant(s) and rewrote %d; the rows as they were are in org_grant_dedupe_log (grant again with `monoagentcli org grant`, `org group init` or `org autonomy set` if a role is missing something)", w.revoked, w.rewritten)
	}
	return nil
}

type grantRow struct {
	id, profile, org, role, tools, orgToolsJSON, created, updated string
	orgTools                                                      bool // an org-tool row, not an automation grant
}

// duplicateOrgGrants returns the live rows that share a slot, each group
// oldest first in the order the runtime reads them. The slot expressions are
// those of migration 173's indexes.
func duplicateOrgGrants(ctx context.Context, tx *sql.Tx) ([][]grantRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at,
		tools_json IN ('[]', 'null'),
		CASE WHEN json_valid(tools_json) THEN json_extract(tools_json, '$[0].alias') END
		FROM org_grants WHERE revoked_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("reading org_grants: %w", err)
	}
	defer rows.Close()
	slots := map[string][]grantRow{}
	var order []string
	for rows.Next() {
		var r grantRow
		var alias any
		if err := rows.Scan(&r.id, &r.profile, &r.org, &r.role, &r.tools, &r.orgToolsJSON, &r.created, &r.updated, &r.orgTools, &alias); err != nil {
			return nil, fmt.Errorf("reading org_grants: %w", err)
		}
		if !r.orgTools && alias == nil {
			continue // no slot: the indexes do not constrain it
		}
		switch v := alias.(type) {
		case int64:
			alias = float64(v) // the index compares numbers as numbers
		case []byte:
			alias = string(v)
		}
		key := fmt.Sprintf("%q %q %q %v %#v", r.profile, r.org, r.role, r.orgTools, alias)
		if _, seen := slots[key]; !seen {
			order = append(order, key)
		}
		slots[key] = append(slots[key], r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading org_grants: %w", err)
	}
	var out [][]grantRow
	for _, k := range order {
		if len(slots[k]) > 1 {
			out = append(out, slots[k])
		}
	}
	return out, nil
}

type grantRewriter struct {
	ctx                context.Context
	tx                 *sql.Tx
	now                string
	revoked, rewritten int
}

func (w *grantRewriter) record(r grantRow, action string, kept any) error {
	_, err := w.tx.ExecContext(w.ctx, `INSERT INTO org_grant_dedupe_log
		(grant_id, profile_id, org_name, role_id, action, kept_grant_id, tools_json, org_tools_json, created_at, updated_at, recorded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.id, r.profile, r.org, r.role, action, kept, r.tools, r.orgToolsJSON, r.created, r.updated, w.now)
	if err != nil {
		return fmt.Errorf("recording org grant %s: %w", r.id, err)
	}
	return nil
}

func (w *grantRewriter) revoke(r grantRow, kept any) error {
	if err := w.record(r, "revoked", kept); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(w.ctx, `UPDATE org_grants SET revoked_at = ?, updated_at = ? WHERE id = ?`, w.now, w.now, r.id); err != nil {
		return fmt.Errorf("revoking duplicate org grant %s: %w", r.id, err)
	}
	w.revoked++
	return nil
}

// rewrite records r and sets one of its JSON columns.
func (w *grantRewriter) rewrite(r grantRow, column string, value []byte) error {
	if err := w.record(r, "rewritten", r.id); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(w.ctx, `UPDATE org_grants SET `+column+` = ?, updated_at = ? WHERE id = ?`, string(value), w.now, r.id); err != nil {
		return fmt.Errorf("rewriting org grant %s: %w", r.id, err)
	}
	w.rewritten++
	return nil
}

// firstTool is the first element of a tools_json array, numbers kept as
// written, and the whole array.
func firstTool(toolsJSON string) (map[string]any, []json.RawMessage) {
	var all []json.RawMessage
	if json.Unmarshal([]byte(toolsJSON), &all) != nil || len(all) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(all[0]))
	dec.UseNumber()
	var t map[string]any
	if dec.Decode(&t) != nil {
		return nil, nil
	}
	return t, all
}

// limit is a numeric field as the runtime reads it: missing or unreadable is 0.
func limit(t map[string]any, field string) int64 {
	n, _ := t[field].(json.Number)
	v, _ := n.Int64()
	return v
}

// tierRank orders the tiers; anything unknown counts as the strictest.
func tierRank(t map[string]any) int {
	switch t["tier"] {
	case "routine":
		return 0
	case "consequential":
		return 1
	}
	return 2
}

func (w *grantRewriter) mergeAutomationRows(g []grantRow) error {
	keep := g[len(g)-1]
	kept, all := firstTool(keep.tools)
	changed := false
	set := func(field string, v any) { kept[field], changed = v, true }
	for _, r := range g[:len(g)-1] {
		other, _ := firstTool(r.tools)
		if kept == nil || other == nil {
			continue
		}
		if other["approval"] == "required" && kept["approval"] != "required" {
			set("approval", "required")
		}
		for _, f := range []string{"max_calls_per_run", "max_calls_per_day"} {
			if v := limit(other, f); v < limit(kept, f) {
				set(f, v)
			}
		}
		if v := limit(other, "max_output_bytes"); v > 0 && v < limit(kept, "max_output_bytes") {
			set("max_output_bytes", v)
		}
		if tierRank(other) > tierRank(kept) {
			set("tier", []string{"routine", "consequential", "irreversible"}[tierRank(other)])
		}
		if h, _ := other["workflow_hash"].(string); h != "" && other["workflow_id"] == kept["workflow_id"] {
			if have, _ := kept["workflow_hash"].(string); have == "" {
				set("workflow_hash", h)
			}
		}
	}
	for _, r := range g[:len(g)-1] {
		if err := w.revoke(r, keep.id); err != nil {
			return err
		}
	}
	if !changed {
		return nil
	}
	first, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	all[0] = first
	value, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return w.rewrite(keep, "tools_json", value)
}

type grantOrgTool struct {
	Tool string   `json:"tool"`
	Orgs []string `json:"orgs"`
}

// commonOrgTools is what every row that holds a tool agrees on; ok is false
// when a row cannot be read.
func commonOrgTools(g []grantRow) (common []grantOrgTool, ok bool) {
	var held [][]grantOrgTool
	for _, r := range g {
		var tools []grantOrgTool
		if json.Unmarshal([]byte(r.orgToolsJSON), &tools) != nil {
			return nil, false
		}
		if len(tools) > 0 {
			held = append(held, tools)
		}
	}
	if len(held) == 0 {
		return nil, true
	}
	// A row's first entry for a tool is the one that counts, as at runtime.
	find := func(tools []grantOrgTool, name string) (grantOrgTool, bool) {
		i := slices.IndexFunc(tools, func(t grantOrgTool) bool { return t.Tool == name })
		if i < 0 {
			return grantOrgTool{}, false
		}
		return tools[i], true
	}
	for i, t := range held[0] {
		if first, _ := find(held[0][:i], t.Tool); first.Tool != "" {
			continue
		}
		orgs := slices.Clone(t.Orgs)
		for _, other := range held[1:] {
			o, _ := find(other, t.Tool)
			orgs = slices.DeleteFunc(orgs, func(org string) bool { return !slices.Contains(o.Orgs, org) })
		}
		if len(orgs) > 0 {
			common = append(common, grantOrgTool{Tool: t.Tool, Orgs: orgs})
		}
	}
	return common, true
}

func (w *grantRewriter) mergeOrgToolRows(g []grantRow) error {
	common, ok := commonOrgTools(g)
	keep, rest, kept := g[0], g[1:], any(g[0].id)
	if !ok || len(common) == 0 {
		rest, kept = g, nil
	}
	for _, r := range rest {
		if err := w.revoke(r, kept); err != nil {
			return err
		}
	}
	if kept == nil {
		return nil
	}
	value, err := json.Marshal(common)
	if err != nil {
		return err
	}
	var before []grantOrgTool
	if json.Unmarshal([]byte(keep.orgToolsJSON), &before) == nil {
		if b, err := json.Marshal(before); err == nil && bytes.Equal(b, value) {
			return nil
		}
	}
	return w.rewrite(keep, "org_tools_json", value)
}
