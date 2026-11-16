package orggrant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Defaults for a new grant (contracts §4).
const (
	DefaultTimeoutSeconds = 600
	DefaultMaxCallsPerRun = 20
	DefaultMaxCallsPerDay = 200
	DefaultMaxOutputBytes = 16384
)

// ErrNotFound is returned when no live row matches.
var ErrNotFound = errors.New("orggrant: not found")

// Tool is one granted automation inside a grant row's tools_json.
type Tool struct {
	Tool       string `json:"tool"` // automation_<alias>
	WorkflowID string `json:"workflow_id"`
	// WorkflowHash is the definition hash the grant approved; empty on a
	// grant made before it existed (see CheckWorkflow).
	WorkflowHash   string `json:"workflow_hash,omitempty"`
	Alias          string `json:"alias"`
	Mode           string `json:"mode"` // run | trigger | status
	Wait           bool   `json:"wait"`
	Timeout        int    `json:"timeout"`  // seconds
	Approval       string `json:"approval"` // none | required
	Tier           string `json:"tier"`
	MaxCallsPerRun int    `json:"max_calls_per_run"`
	MaxCallsPerDay int    `json:"max_calls_per_day"`
	MaxOutputBytes int    `json:"max_output_bytes"`
}

// OrgTool is a holding-org or decider tool inside org_tools_json.
type OrgTool struct {
	Tool string   `json:"tool"` // org_start | org_stop | org_status | org_report | decision_list | decision_resolve
	Orgs []string `json:"orgs"`
}

// Grant is one org_grants row. Automation grants carry exactly one Tool;
// org-tool grants carry OrgTools and no Tools.
type Grant struct {
	ID        string
	ProfileID string
	OrgName   string
	RoleID    string
	Tools     []Tool
	OrgTools  []OrgTool
	CreatedAt time.Time
	UpdatedAt time.Time
	RevokedAt *time.Time
}

// Automation returns the grant's automation tool, or nil for an org-tool
// grant.
func (g *Grant) Automation() *Tool {
	if len(g.Tools) == 0 {
		return nil
	}
	return &g.Tools[0]
}

// Store reads and writes org_grants and org_endpoints.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db (migration 041 applied).
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// GrantInput describes an automation grant to create or update.
type GrantInput struct {
	ProfileID string
	OrgName   string
	RoleID    string
	Tool      Tool // Alias, WorkflowID required; zero limits take defaults
}

func normalizeTool(t Tool) Tool {
	t.Tool = "automation_" + t.Alias
	if t.Mode == "" {
		t.Mode = "run"
	}
	if t.Timeout <= 0 {
		t.Timeout = DefaultTimeoutSeconds
	}
	if t.Approval == "" {
		t.Approval = "none"
	}
	if t.MaxCallsPerRun <= 0 {
		t.MaxCallsPerRun = DefaultMaxCallsPerRun
	}
	if t.MaxCallsPerDay <= 0 {
		t.MaxCallsPerDay = DefaultMaxCallsPerDay
	}
	if t.MaxOutputBytes <= 0 {
		t.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return t
}

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
	now := nowString(s.now())
	existing, err := s.findAutomationGrant(ctx, in.ProfileID, in.OrgName, in.RoleID, tool.Alias)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE org_grants SET tools_json = ?, updated_at = ? WHERE id = ?`,
			string(toolsJSON), now, existing.ID); err != nil {
			return nil, fmt.Errorf("orggrant: update grant: %w", err)
		}
		return s.GetGrant(ctx, existing.ID)
	}
	id := NewGrantID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '[]', ?, ?)`,
		id, in.ProfileID, in.OrgName, in.RoleID, string(toolsJSON), now, now); err != nil {
		return nil, fmt.Errorf("orggrant: insert grant: %w", err)
	}
	return s.GetGrant(ctx, id)
}

// SetOrgTools replaces the live org-tool grant for (profile, org, role);
// an empty tools list revokes it.
func (s *Store) SetOrgTools(ctx context.Context, profileID, org, role string, tools []OrgTool) (*Grant, error) {
	now := nowString(s.now())
	grants, err := s.ListGrants(ctx, profileID, org, role)
	if err != nil {
		return nil, err
	}
	var existing *Grant
	for i := range grants {
		if len(grants[i].Tools) == 0 {
			existing = &grants[i]
			break
		}
	}
	if len(tools) == 0 {
		if existing != nil {
			_, err := s.db.ExecContext(ctx, `UPDATE org_grants SET revoked_at = ?, updated_at = ? WHERE id = ?`, now, now, existing.ID)
			return nil, err
		}
		return nil, nil
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE org_grants SET org_tools_json = ?, updated_at = ? WHERE id = ?`, string(b), now, existing.ID); err != nil {
			return nil, err
		}
		return s.GetGrant(ctx, existing.ID)
	}
	id := NewGrantID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO org_grants (id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '[]', ?, ?, ?)`,
		id, profileID, org, role, string(b), now, now); err != nil {
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
	grants, err := s.ListGrants(ctx, profileID, org, role)
	if err != nil {
		return nil, err
	}
	named := map[string]bool{}
	for _, t := range tools {
		named[t.Tool] = true
	}
	var out []OrgTool
	for _, g := range grants {
		if len(g.Tools) > 0 {
			continue
		}
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
	return s.SetOrgTools(ctx, profileID, org, role, out)
}

// MergeOrgTools adds tools (and the orgs each may act on) to a role's
// org-tool grant, keeping what it already holds.
func (s *Store) MergeOrgTools(ctx context.Context, profileID, org, role string, add []OrgTool) (*Grant, error) {
	grants, err := s.ListGrants(ctx, profileID, org, role)
	if err != nil {
		return nil, err
	}
	var tools []OrgTool
	for _, g := range grants {
		if len(g.Tools) == 0 {
			tools = append(tools, g.OrgTools...)
		}
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
	return s.SetOrgTools(ctx, profileID, org, role, tools)
}

const grantCols = `id, profile_id, org_name, role_id, tools_json, org_tools_json, created_at, updated_at, revoked_at`

func scanGrant(sc interface{ Scan(...interface{}) error }) (*Grant, error) {
	var g Grant
	var tools, orgTools, created, updated string
	var revoked sql.NullString
	if err := sc.Scan(&g.ID, &g.ProfileID, &g.OrgName, &g.RoleID, &tools, &orgTools, &created, &updated, &revoked); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tools), &g.Tools); err != nil {
		return nil, fmt.Errorf("orggrant: grant %s tools_json: %w", g.ID, err)
	}
	if err := json.Unmarshal([]byte(orgTools), &g.OrgTools); err != nil {
		return nil, fmt.Errorf("orggrant: grant %s org_tools_json: %w", g.ID, err)
	}
	g.CreatedAt = parseTime(created)
	g.UpdatedAt = parseTime(updated)
	if revoked.Valid && revoked.String != "" {
		t := parseTime(revoked.String)
		g.RevokedAt = &t
	}
	return &g, nil
}

// GetGrant returns a grant by id, revoked or not.
func (s *Store) GetGrant(ctx context.Context, id string) (*Grant, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+grantCols+` FROM org_grants WHERE id = ?`, id)
	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

// ListGrants returns live grants for (profile, org), narrowed to role when
// role is non-empty, oldest first.
func (s *Store) ListGrants(ctx context.Context, profileID, org, role string) ([]Grant, error) {
	q := `SELECT ` + grantCols + ` FROM org_grants WHERE profile_id = ? AND org_name = ? AND revoked_at IS NULL`
	args := []interface{}{profileID, org}
	if role != "" {
		q += ` AND role_id = ?`
		args = append(args, role)
	}
	q += ` ORDER BY created_at, id`
	return s.queryGrants(ctx, q, args...)
}

// ListProfileGrants returns every live grant in a profile.
func (s *Store) ListProfileGrants(ctx context.Context, profileID string) ([]Grant, error) {
	return s.queryGrants(ctx, `SELECT `+grantCols+` FROM org_grants WHERE profile_id = ? AND revoked_at IS NULL ORDER BY org_name, role_id, created_at, id`, profileID)
}

// GrantsForWorkflow returns every live automation grant (any profile) that
// targets workflowID — used to refuse or cascade workflow deletion (C-20).
func (s *Store) GrantsForWorkflow(ctx context.Context, workflowID string) ([]Grant, error) {
	all, err := s.queryGrants(ctx, `SELECT `+grantCols+` FROM org_grants WHERE revoked_at IS NULL AND tools_json LIKE ? ORDER BY created_at`, "%"+workflowID+"%")
	if err != nil {
		return nil, err
	}
	var out []Grant
	for _, g := range all {
		if t := g.Automation(); t != nil && t.WorkflowID == workflowID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *Store) queryGrants(ctx context.Context, q string, args ...interface{}) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("orggrant: list grants: %w", err)
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *Store) findAutomationGrant(ctx context.Context, profileID, org, role, alias string) (*Grant, error) {
	grants, err := s.ListGrants(ctx, profileID, org, role)
	if err != nil {
		return nil, err
	}
	for i := range grants {
		if t := grants[i].Automation(); t != nil && t.Alias == alias {
			return &grants[i], nil
		}
	}
	return nil, ErrNotFound
}

// RevokeGrant revokes the live grant for (profile, org, role, alias).
func (s *Store) RevokeGrant(ctx context.Context, profileID, org, role, alias string) error {
	g, err := s.findAutomationGrant(ctx, profileID, org, role, alias)
	if err != nil {
		return err
	}
	return s.revokeByID(ctx, g.ID)
}

func (s *Store) revokeByID(ctx context.Context, id string) error {
	now := nowString(s.now())
	_, err := s.db.ExecContext(ctx, `UPDATE org_grants SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL`, now, now, id)
	return err
}

// RevokeAlias revokes every live grant in the org that targets alias.
func (s *Store) RevokeAlias(ctx context.Context, profileID, org, alias string) (int, error) {
	grants, err := s.ListGrants(ctx, profileID, org, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, g := range grants {
		if t := g.Automation(); t != nil && t.Alias == alias {
			if err := s.revokeByID(ctx, g.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// Bundle is the scope a grant-mode MCP server serves: every live grant of
// the (profile, org, role) the bundle id's row belongs to (contracts §7).
type Bundle struct {
	ID        string
	ProfileID string
	OrgName   string
	RoleID    string
	Grants    []Grant
}

// ResolveBundle returns the scope for a --grant id. The row may itself be
// revoked (the JSON still names it until the next reconcile); the scope is
// taken from the row, never from the caller.
func (s *Store) ResolveBundle(ctx context.Context, id string) (*Bundle, error) {
	g, err := s.GetGrant(ctx, id)
	if err != nil {
		return nil, err
	}
	live, err := s.ListGrants(ctx, g.ProfileID, g.OrgName, g.RoleID)
	if err != nil {
		return nil, err
	}
	return &Bundle{ID: id, ProfileID: g.ProfileID, OrgName: g.OrgName, RoleID: g.RoleID, Grants: live}, nil
}

// RenameOrg moves every row of org to newName within a profile (C-23).
func (s *Store) RenameOrg(ctx context.Context, profileID, org, newName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"org_grants", "org_endpoints", "org_autonomy", "org_decisions", "org_asks", "org_delegations"} {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET org_name = ? WHERE profile_id = ? AND org_name = ?`, newName, profileID, org); err != nil {
			return fmt.Errorf("orggrant: rename in %s: %w", table, err)
		}
	}
	// A delegation also names the org of the deciding role. Left on the old
	// name, PendingFor stops showing the item to its decider and
	// SweepDelegations cannot resolve it, so it is never decided.
	if _, err := tx.ExecContext(ctx, `UPDATE org_delegations SET decider_org = ? WHERE profile_id = ? AND decider_org = ?`, newName, profileID, org); err != nil {
		return fmt.Errorf("orggrant: rename decider in org_delegations: %w", err)
	}
	return tx.Commit()
}

// RevokeOrg revokes every live grant and endpoint of org and drops the
// state an org of the same name must never inherit (org delete): the
// autonomy row — level, decider, tier overrides, policy and paused_until —
// so a new org starts at mid again (Q8), and every delegation the deleted
// org can no longer take part in. org_decisions rows stay: they are the
// audit ledger of what the org did. org_asks rows stay too — the waker
// times a waiting ask out at its deadline and resumes the execution that
// waits on it, which expiring the row here would skip.
func (s *Store) RevokeOrg(ctx context.Context, profileID, org string) error {
	now := nowString(s.now())
	if _, err := s.db.ExecContext(ctx, `UPDATE org_grants SET revoked_at = ?, updated_at = ? WHERE profile_id = ? AND org_name = ? AND revoked_at IS NULL`, now, now, profileID, org); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE org_endpoints SET revoked_at = ? WHERE profile_id = ? AND org_name = ? AND `+usableEndpoint, now, profileID, org, now); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM org_autonomy WHERE profile_id = ? AND org_name = ?`, profileID, org); err != nil {
		return fmt.Errorf("orggrant: clear autonomy: %w", err)
	}
	// Pending either way round: the item belongs to the deleted org, or it
	// waits on a decider role inside it. Neither can ever be resolved —
	// SweepDelegations gives up on the missing org — and both would
	// resurface to the next org of this name.
	_, err := s.db.ExecContext(ctx,
		`UPDATE org_delegations SET status = 'expired', resolved_at = ?
		 WHERE profile_id = ? AND status = 'pending' AND (org_name = ? OR decider_org = ?)`,
		now, profileID, org, org)
	return err
}
