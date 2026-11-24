package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// ---------------------------------------------------------------------------
// Agent organizations (internal/orgdesign — Org Runtime v2 designs)
//
// Every handler here is a thin Load -> orgdesign mutator -> Save adapter.
// Save validates internally and returns a plain Go error on an invalid
// result (e.g. a structural problem AddRole/SetReportsTo didn't already
// catch); that error is returned as-is, matching every other mutation in
// this file (checkWorkflowOwnership, secrets.Add, ...) — the chat
// protocol's tool_result frame surfaces err.Error() to the model either
// way, so a failed mutation reads the same as any other tool error and the
// model can react and retry within the turn.
// ---------------------------------------------------------------------------

type roleView struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Type             string   `json:"type"`
	ReportsTo        *string  `json:"reports_to"`
	Responsibilities []string `json:"responsibilities"`
}

func roleToView(r orgdesign.Role) roleView {
	return roleView{
		ID:               r.ID,
		Title:            r.Title,
		Type:             r.Type,
		ReportsTo:        r.ReportsTo,
		Responsibilities: r.Responsibilities,
	}
}

type orgSummary struct {
	Name      string          `json:"name"`
	Goal      string          `json:"goal"`
	Status    string          `json:"status"`
	Schedule  json.RawMessage `json:"schedule,omitempty"`
	RoleCount int             `json:"role_count"`
}

func (mt *MonoagentTools) listOrgs(string) (string, error) {
	root := mt.profileRoot()
	names, err := orgdesign.ListOrgNames(root)
	if err != nil {
		return "", fmt.Errorf("list orgs: %w", err)
	}
	out := make([]orgSummary, 0, len(names))
	for _, name := range names {
		doc, err := orgdesign.Load(root, name)
		if err != nil {
			// Skip a config that fails to parse rather than failing the
			// whole listing — the model can still see and work with every
			// other org.
			continue
		}
		out = append(out, orgSummary{
			Name:      doc.Name,
			Goal:      doc.Goal,
			Status:    doc.Status,
			Schedule:  doc.Schedule,
			RoleCount: len(doc.Roles),
		})
	}
	return marshalJSON(map[string]interface{}{"orgs": out})
}

type orgNameArgs struct {
	OrgName string `json:"org_name"`
}

func (mt *MonoagentTools) getOrg(args string) (string, error) {
	var a orgNameArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	doc, err := orgdesign.Load(mt.profileRoot(), a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}
	roles := make([]roleView, 0, len(doc.Roles))
	for _, r := range doc.Roles {
		roles = append(roles, roleToView(r))
	}
	return marshalJSON(map[string]interface{}{
		"name":     doc.Name,
		"goal":     doc.Goal,
		"status":   doc.Status,
		"schedule": doc.Schedule,
		"runtime":  doc.Runtime,
		"roles":    roles,
	})
}

// lockOrgRows holds the profile's org rows lock for one load-write-save, the
// lock `org` commands and the daemon's reconcile take, so a document loaded
// here cannot be saved over a concurrent edit.
func lockOrgRows(root string) (func(), error) {
	release, err := orgenv.LockRows(root)
	if err != nil {
		return nil, fmt.Errorf("org rows lock: %w", err)
	}
	return release, nil
}

type createOrgArgs struct {
	Name          string `json:"name"`
	Goal          string `json:"goal"`
	Schedule      string `json:"schedule"`
	Runtime       string `json:"runtime"`
	Workspace     string `json:"workspace"`
	RootRoleID    string `json:"root_role_id"`
	RootRoleTitle string `json:"root_role_title"`
}

func (mt *MonoagentTools) createOrg(args string) (string, error) {
	var a createOrgArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	root := mt.profileRoot()
	release, err := lockOrgRows(root)
	if err != nil {
		return "", err
	}
	defer release()
	path, err := orgdesign.ConfigPath(root, a.Name)
	if err != nil {
		return "", fmt.Errorf("create org: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("an org named %q already exists", a.Name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking for existing org %q: %w", a.Name, err)
	}

	opts := orgdesign.NewOrgOptions{
		Runtime:           a.Runtime,
		Workspace:         a.Workspace,
		RootRoleID:        a.RootRoleID,
		RootRoleTitle:     a.RootRoleTitle,
		RestrictFileWrite: mt.restrictFileWriteForOrgs(),
	}
	if a.Schedule != "" {
		sched, err := json.Marshal(a.Schedule)
		if err != nil {
			return "", fmt.Errorf("encode schedule: %w", err)
		}
		opts.Schedule = sched
	}
	doc := orgdesign.NewOrg(a.Name, a.Goal, opts)
	if _, err := orgdesign.Save(root, doc); err != nil {
		return "", fmt.Errorf("save org %s: %w", a.Name, err)
	}
	// New orgs start at autonomy level mid (plan Q8); existing orgs without
	// a row stay manual.
	if mt.db != nil && mt.orgProjectRootOverride() == "" {
		store := orgdecide.NewStore(mt.db)
		if row, err := store.Get(context.Background(), mt.ProfileID(), doc.Name); err == nil && !row.Stored {
			row.Level = orgdesign.LevelMid
			_ = store.Put(context.Background(), row, "chat")
		}
	}
	return marshalJSON(map[string]interface{}{"org_name": doc.Name, "created": true})
}

type addOrgRoleArgs struct {
	OrgName          string   `json:"org_name"`
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Type             string   `json:"type"`
	ReportsTo        string   `json:"reports_to"`
	Responsibilities []string `json:"responsibilities"`
	Model            string   `json:"model"`
	Runtime          string   `json:"runtime"`
	Icon             string   `json:"icon"`
}

func (mt *MonoagentTools) addOrgRole(args string) (string, error) {
	var a addOrgRoleArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	root := mt.profileRoot()
	release, err := lockOrgRows(root)
	if err != nil {
		return "", err
	}
	defer release()
	doc, err := orgdesign.Load(root, a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}

	role := orgdesign.Role{
		ID:               a.ID,
		Title:            a.Title,
		Type:             a.Type,
		Responsibilities: a.Responsibilities,
	}
	if a.ReportsTo != "" {
		reportsTo := a.ReportsTo
		role.ReportsTo = &reportsTo
	}
	if a.Model != "" {
		modelJSON, err := json.Marshal(a.Model)
		if err != nil {
			return "", fmt.Errorf("encode model: %w", err)
		}
		role.AdapterConfig = map[string]json.RawMessage{"model": modelJSON}
	}
	if a.Runtime != "" {
		runtimeJSON, err := json.Marshal(a.Runtime)
		if err != nil {
			return "", fmt.Errorf("encode runtime: %w", err)
		}
		role.Extra = map[string]json.RawMessage{"runtime": runtimeJSON}
	}
	if a.Icon != "" {
		role.UI = &orgdesign.RoleUI{Icon: a.Icon}
	}

	added, err := doc.AddRole(role, mt.restrictFileWriteForOrgs())
	if err != nil {
		return "", fmt.Errorf("add role to org %s: %w", a.OrgName, err)
	}
	if _, err := orgdesign.Save(root, doc); err != nil {
		return "", fmt.Errorf("save org %s: %w", a.OrgName, err)
	}
	return marshalJSON(map[string]interface{}{"org_name": a.OrgName, "role": roleToView(*added)})
}

type updateOrgRoleArgs struct {
	OrgName          string    `json:"org_name"`
	RoleID           string    `json:"role_id"`
	Title            *string   `json:"title"`
	Type             *string   `json:"type"`
	Responsibilities *[]string `json:"responsibilities"`
	Model            *string   `json:"model"`
	Runtime          *string   `json:"runtime"`
	Icon             *string   `json:"icon"`
}

func (mt *MonoagentTools) updateOrgRole(args string) (string, error) {
	var a updateOrgRoleArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	root := mt.profileRoot()
	release, err := lockOrgRows(root)
	if err != nil {
		return "", err
	}
	defer release()
	doc, err := orgdesign.Load(root, a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}

	patch := orgdesign.RolePatch{
		Title:            a.Title,
		Type:             a.Type,
		Responsibilities: a.Responsibilities,
		Runtime:          a.Runtime,
		Model:            a.Model,
		Icon:             a.Icon,
	}
	updated, err := doc.UpdateRole(a.RoleID, patch)
	if err != nil {
		return "", fmt.Errorf("update role in org %s: %w", a.OrgName, err)
	}
	if _, err := orgdesign.Save(root, doc); err != nil {
		return "", fmt.Errorf("save org %s: %w", a.OrgName, err)
	}
	return marshalJSON(map[string]interface{}{"org_name": a.OrgName, "role": roleToView(*updated)})
}

type setRoleReportsToArgs struct {
	OrgName   string `json:"org_name"`
	RoleID    string `json:"role_id"`
	ReportsTo string `json:"reports_to"`
}

func (mt *MonoagentTools) setRoleReportsTo(args string) (string, error) {
	var a setRoleReportsToArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	root := mt.profileRoot()
	release, err := lockOrgRows(root)
	if err != nil {
		return "", err
	}
	defer release()
	doc, err := orgdesign.Load(root, a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}
	if err := doc.SetReportsTo(a.RoleID, a.ReportsTo); err != nil {
		return "", fmt.Errorf("move role in org %s: %w", a.OrgName, err)
	}
	if _, err := orgdesign.Save(root, doc); err != nil {
		return "", fmt.Errorf("save org %s: %w", a.OrgName, err)
	}
	return marshalJSON(map[string]interface{}{"org_name": a.OrgName, "role_id": a.RoleID, "reports_to": a.ReportsTo})
}

type removeOrgRoleArgs struct {
	OrgName  string `json:"org_name"`
	RoleID   string `json:"role_id"`
	Strategy string `json:"strategy"`
	Confirm  bool   `json:"confirm"`
}

// subtreeIDs walks doc.Children starting at id (breadth-first), returning
// every descendant's id — used to preview a cascade deletion without
// mutating doc, since orgdesign's own descendantsOf is unexported (private
// to RemoveRole's Cascade branch).
func subtreeIDs(doc *orgdesign.Doc, id string) []string {
	var out []string
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range doc.Children(cur) {
			out = append(out, c.ID)
			queue = append(queue, c.ID)
		}
	}
	return out
}

func (mt *MonoagentTools) removeOrgRole(args string) (string, error) {
	var a removeOrgRoleArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	strategy := orgdesign.RemoveStrategy(a.Strategy)
	if strategy == "" {
		strategy = orgdesign.Reparent
	}

	root := mt.profileRoot()
	release, err := lockOrgRows(root)
	if err != nil {
		return "", err
	}
	defer release()
	doc, err := orgdesign.Load(root, a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}

	if strategy == orgdesign.Cascade && !a.Confirm {
		return marshalJSON(map[string]interface{}{
			"would_remove": true,
			"org_name":     a.OrgName,
			"role_id":      a.RoleID,
			"strategy":     "cascade",
			"also_removes": subtreeIDs(doc, a.RoleID),
			"note":         "cascade removal of this role and its subtree requires confirm:true; call again with confirm:true to actually perform it",
		})
	}

	removed, err := doc.RemoveRole(a.RoleID, strategy)
	if err != nil {
		return "", fmt.Errorf("remove role from org %s: %w", a.OrgName, err)
	}
	if _, err := orgdesign.Save(root, doc); err != nil {
		return "", fmt.Errorf("save org %s: %w", a.OrgName, err)
	}
	return marshalJSON(map[string]interface{}{"org_name": a.OrgName, "removed_role_ids": removed})
}

func (mt *MonoagentTools) validateOrg(args string) (string, error) {
	var a orgNameArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	doc, err := orgdesign.Load(mt.profileRoot(), a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}
	if err := orgdesign.Validate(doc); err != nil {
		return marshalJSON(map[string]interface{}{
			"org_name": a.OrgName,
			"valid":    false,
			"error":    err.Error(),
		})
	}
	return marshalJSON(map[string]interface{}{"org_name": a.OrgName, "valid": true})
}

type reloadOrgArgs struct {
	OrgName string `json:"org_name"`
	Confirm bool   `json:"confirm"`
}

func (mt *MonoagentTools) reloadOrg(args string) (string, error) {
	var a reloadOrgArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	root := mt.profileRoot()
	release, err := lockOrgRows(root)
	if err != nil {
		return "", err
	}
	defer release()
	doc, err := orgdesign.Load(root, a.OrgName)
	if err != nil {
		return "", fmt.Errorf("load org %s: %w", a.OrgName, err)
	}
	running := doc.Status == "running"

	if !a.Confirm {
		return marshalJSON(map[string]interface{}{
			"would_reload": true,
			"org_name":     a.OrgName,
			"running":      running,
			"note":         fmt.Sprintf("would reload %s — pass confirm:true to actually do it", a.OrgName),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := monomind.OrgReload(ctx, root, a.OrgName)
	if err != nil {
		return "", fmt.Errorf("reload org %s: %w", a.OrgName, err)
	}
	return marshalJSON(map[string]interface{}{"org_name": a.OrgName, "reloaded": true, "output": out})
}
