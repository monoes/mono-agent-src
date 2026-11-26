package orggrant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// The writers below read the live rows and write them on one connection inside
// a BEGIN IMMEDIATE transaction, so two writers cannot both act on what only
// one of them may. Migration 172's unique indexes hold the same rule in the
// database: one live row per (profile, org, role, alias), and one live
// org-tool row per (profile, org, role).

// UpsertGrant creates the live grant for (profile, org, role, alias), or
// updates it in place when one exists — its id is kept so the provider args
// already written into the org JSON stay valid.
func (s *Store) UpsertGrant(ctx context.Context, in GrantInput) (*Grant, error) {
	if in.ProfileID == "" || in.OrgName == "" || in.RoleID == "" || in.Tool.Alias == "" || in.Tool.WorkflowID == "" {
		return nil, fmt.Errorf("orggrant: profile, org, role, alias, and workflow are required")
	}
	tool := normalizeTool(in.Tool)
	toolsJSON, err := json.Marshal([]Tool{tool})
	if err != nil {
		return nil, err
	}
	var id string
	err = dbconn.WithWriteTx(ctx, s.db, func(c *sql.Conn) error {
		now := nowString(s.now())
		existing, err := findAutomationGrant(ctx, c, in.ProfileID, in.OrgName, in.RoleID, tool.Alias)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if existing != nil {
			id = existing.ID
			if _, err := c.ExecContext(ctx,
				`UPDATE org_grants SET tools_json = ?, updated_at = ? WHERE id = ?`,
				string(toolsJSON), now, id); err != nil {
				return fmt.Errorf("orggrant: update grant: %w", err)
			}
			return nil
		}
		id = NewGrantID()
		if _, err := c.ExecContext(ctx,
			`INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, '[]', ?, ?)`,
			id, in.ProfileID, in.OrgName, in.RoleID, string(toolsJSON), now, now); err != nil {
			return fmt.Errorf("orggrant: insert grant: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetGrant(ctx, id)
}

// SetOrgTools replaces the live org-tool grant for (profile, org, role);
// an empty tools list revokes it.
func (s *Store) SetOrgTools(ctx context.Context, profileID, org, role string, tools []OrgTool) (*Grant, error) {
	return s.writeOrgTools(ctx, profileID, org, role, func([]Grant) []OrgTool { return tools })
}

// writeOrgTools replaces a role's org-tool grant with what next makes of the
// live org-tool rows. One row is kept and rewritten; any other is revoked, so
// no older scope stays in force. An empty result revokes them all and returns
// no grant.
func (s *Store) writeOrgTools(ctx context.Context, profileID, org, role string, next func([]Grant) []OrgTool) (*Grant, error) {
	var id string
	err := dbconn.WithWriteTx(ctx, s.db, func(c *sql.Conn) error {
		now := nowString(s.now())
		grants, err := listGrants(ctx, c, profileID, org, role)
		if err != nil {
			return err
		}
		var rows []Grant
		for _, g := range grants {
			if len(g.Tools) == 0 {
				rows = append(rows, g)
			}
		}
		tools := next(rows)
		if len(tools) > 0 && len(rows) > 0 {
			id, rows = rows[0].ID, rows[1:]
		}
		for _, g := range rows {
			if _, err := c.ExecContext(ctx, `UPDATE org_grants SET revoked_at = ?, updated_at = ? WHERE id = ?`, now, now, g.ID); err != nil {
				return err
			}
		}
		if len(tools) == 0 {
			return nil
		}
		b, err := json.Marshal(tools)
		if err != nil {
			return err
		}
		if id != "" {
			_, err = c.ExecContext(ctx, `UPDATE org_grants SET org_tools_json = ?, updated_at = ? WHERE id = ?`, string(b), now, id)
			return err
		}
		id = NewGrantID()
		_, err = c.ExecContext(ctx,
			`INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at)
			 VALUES (?, ?, ?, ?, '[]', ?, ?, ?)`,
			id, profileID, org, role, string(b), now, now)
		return err
	})
	if err != nil || id == "" {
		return nil, err
	}
	return s.GetGrant(ctx, id)
}

// SetOrgToolScope sets the exact orgs each tool it names may act on,
// leaving tools it does not name alone and dropping a named tool left with
// no orgs. It is for callers that know a tool's whole current scope —
// `org group init`, which passes every Initiator tool over the holding
// org's children, so dropping a child takes the child back off the
// Initiator. MergeOrgTools stays additive for callers that only ever know
// one org at a time, such as a decider configured per child org.
func (s *Store) SetOrgToolScope(ctx context.Context, profileID, org, role string, tools []OrgTool) (*Grant, error) {
	named := map[string]bool{}
	for _, t := range tools {
		named[t.Tool] = true
	}
	return s.writeOrgTools(ctx, profileID, org, role, func(rows []Grant) []OrgTool {
		var out []OrgTool
		for _, g := range rows {
			for _, ot := range g.OrgTools {
				if !named[ot.Tool] {
					out = append(out, ot)
				}
			}
		}
		for _, t := range tools {
			if len(t.Orgs) > 0 {
				out = append(out, t)
			}
		}
		return out
	})
}

// MergeOrgTools adds tools (and the orgs each may act on) to a role's
// org-tool grant, keeping what it already holds.
func (s *Store) MergeOrgTools(ctx context.Context, profileID, org, role string, add []OrgTool) (*Grant, error) {
	return s.writeOrgTools(ctx, profileID, org, role, func(rows []Grant) []OrgTool {
		var tools []OrgTool
		for _, g := range rows {
			tools = append(tools, g.OrgTools...)
		}
		for _, a := range add {
			idx := -1
			for i := range tools {
				if tools[i].Tool == a.Tool {
					idx = i
				}
			}
			if idx < 0 {
				tools = append(tools, OrgTool{Tool: a.Tool})
				idx = len(tools) - 1
			}
			for _, o := range a.Orgs {
				found := false
				for _, have := range tools[idx].Orgs {
					if have == o {
						found = true
					}
				}
				if !found {
					tools[idx].Orgs = append(tools[idx].Orgs, o)
				}
			}
		}
		return tools
	})
}
