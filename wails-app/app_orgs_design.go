package main

import (
	"encoding/json"
	"fmt"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// ─────────────────────────────────────────────────────────────────────────────
// Org Designer — read/write org config files directly
//
// This file necessarily breaks app_orgs.go's doctrine ("never import
// monomind internals, every call shells out to `monoagentcli org ...`"):
// there is no `monomind org` subcommand for editing an existing org's roles
// or hierarchy (`org create` is a template scaffolder with no --file/stdin
// spec — confirmed against the CLI's own usage string), so direct JSON file
// mutation via internal/orgdesign is the only mutation path available. That
// package handles round-trip fidelity (unknown fields survive a save) and
// its own structural validation (including cycle detection, which
// monomind's own `org validate` does not perform).
//
// Every mutation here still runs `monoagentcli org validate` as an
// authoritative second check after writing — the Zod schema is the source
// of truth for anything this package's Go model doesn't know about — and
// rolls back to the pre-write bytes on failure, so a config that monomind
// itself can't load is never left on disk (see saveAndValidate).
// ─────────────────────────────────────────────────────────────────────────────

// GetOrgDesign returns one org's full design (roles, hierarchy, canvas
// layout) plus validation status, in one round trip for the canvas to load.
func (a *App) GetOrgDesign(orgName string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	valid, errs := a.validateDoc(d)
	b, err := json.Marshal(map[string]interface{}{
		"v":   1,
		"org": d,
		// Go decides whether the org is a sections org (Doc.SectionsEnabled).
		"sections_enabled": d.SectionsEnabled(),
		"valid":            valid,
		"errors":           errs,
	})
	if err != nil {
		return aiError(err)
	}
	return string(b)
}

// ListOrgDesigns lists every org config file under the active profile's
// orgs directory — distinct from ListOrgs (app_orgs.go), which proxies
// `org list`'s runtime state: this reads config files directly, so it
// includes designs that have never been run.
func (a *App) ListOrgDesigns() string {
	root := a.orgDesignRoot()
	names, err := orgdesign.ListOrgNames(root)
	if err != nil {
		return aiError(err)
	}
	type item struct {
		Name      string `json:"name"`
		Goal      string `json:"goal"`
		Status    string `json:"status"`
		RoleCount int    `json:"roleCount"`
		// Kind is "holding" for a holding org (plan §6.1), else "" or
		// "standard" — the org list uses it to offer the group view.
		Kind string `json:"kind,omitempty"`
	}
	items := make([]item, 0, len(names))
	for _, name := range names {
		d, err := orgdesign.Load(root, name)
		if err != nil {
			continue // skip a config that fails to parse rather than failing the whole list
		}
		items = append(items, item{Name: d.Name, Goal: d.Goal, Status: d.Status, RoleCount: len(d.Roles), Kind: orgDocKind(d)})
	}
	b, err := json.Marshal(map[string]interface{}{"v": 1, "items": items})
	if err != nil {
		return aiError(err)
	}
	return string(b)
}

// orgDocKind reads the top-level `kind` key from a doc's JSON form, so it
// works whether orgdesign keeps `kind` in Extra or models it as a field.
func orgDocKind(d *orgdesign.Doc) string {
	b, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(b, &probe) != nil {
		return ""
	}
	return probe.Kind
}

// CreateOrgDesign creates a new org with a single root role. specJSON:
// {"name","goal","schedule"?,"runtime"?,"workspace"?,"rootRoleId"?,"rootRoleTitle"?}
func (a *App) CreateOrgDesign(specJSON string) string {
	var spec struct {
		Name          string `json:"name"`
		Goal          string `json:"goal"`
		Schedule      string `json:"schedule"`
		Runtime       string `json:"runtime"`
		Workspace     string `json:"workspace"`
		RootRoleID    string `json:"rootRoleId"`
		RootRoleTitle string `json:"rootRoleTitle"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return aiError(fmt.Errorf("invalid spec: %w", err))
	}
	root := a.orgDesignRoot()
	if _, err := orgdesign.Load(root, spec.Name); err == nil {
		return aiError(fmt.Errorf("an org named %q already exists", spec.Name))
	}
	var scheduleRaw json.RawMessage
	if spec.Schedule != "" {
		scheduleRaw, _ = json.Marshal(spec.Schedule)
	}
	d := orgdesign.NewOrg(spec.Name, spec.Goal, orgdesign.NewOrgOptions{
		Schedule:          scheduleRaw,
		Runtime:           spec.Runtime,
		Workspace:         spec.Workspace,
		RootRoleID:        spec.RootRoleID,
		RootRoleTitle:     spec.RootRoleTitle,
		RestrictFileWrite: a.restrictFileWriteForOrgs(),
	})
	return a.saveAndRespondNew(root, d, "ui", true)
}

// DeleteOrgDesign removes an org's config file only — its run-data
// subdirectory (bus.jsonl, checkpoints, sentinels) is left in place; a full
// teardown belongs to `monomind org delete` via the existing app_orgs.go
// surface, not this method.
func (a *App) DeleteOrgDesign(orgName string) string {
	root := a.orgDesignRoot()
	if err := orgdesign.Delete(root, orgName); err != nil {
		return aiError(err)
	}
	a.emitOrgDesignUpdated(orgName, "ui", true, nil, true, nil)
	return `{"ok":true}`
}

// AddOrgRole adds a role to an org. roleJSON is a orgdesign.Role literal
// (id may be omitted to auto-derive one from title).
func (a *App) AddOrgRole(orgName, roleJSON string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	var r orgdesign.Role
	if err := json.Unmarshal([]byte(roleJSON), &r); err != nil {
		return aiError(fmt.Errorf("invalid role: %w", err))
	}
	if _, err := d.AddRole(r, a.restrictFileWriteForOrgs()); err != nil {
		return aiError(err)
	}
	return a.saveAndRespond(root, d, "ui")
}

// UpdateOrgRole applies a shallow patch to an existing role. patchJSON keys
// present overwrite; keys absent are left unchanged.
func (a *App) UpdateOrgRole(orgName, roleID, patchJSON string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	var raw struct {
		Title                  *string          `json:"title"`
		Type                   *string          `json:"type"`
		Responsibilities       *[]string        `json:"responsibilities"`
		Runtime                *string          `json:"runtime"`
		Model                  *string          `json:"model"`
		Icon                   *string          `json:"icon"`
		Color                  *string          `json:"color"`
		MaxTurnsPerMessage     *int             `json:"max_turns_per_message"`
		BudgetTokens           *int             `json:"budget_tokens"`
		BudgetUSD              *float64         `json:"budget_usd"`
		Policy                 *json.RawMessage `json:"policy"`
		Provider               *json.RawMessage `json:"provider"`
		InstructionsFile       *string          `json:"instructions_file"`
		AdapterConfigProvider  *string          `json:"adapter_config_provider"`
		AdapterConfigMaxTokens *int             `json:"adapter_config_max_tokens"`
	}
	if err := json.Unmarshal([]byte(patchJSON), &raw); err != nil {
		return aiError(fmt.Errorf("invalid patch: %w", err))
	}
	patch := orgdesign.RolePatch{
		Title: raw.Title, Type: raw.Type, Responsibilities: raw.Responsibilities,
		Runtime: raw.Runtime, Model: raw.Model, Icon: raw.Icon, Color: raw.Color,
		MaxTurnsPerMessage: raw.MaxTurnsPerMessage, BudgetTokens: raw.BudgetTokens, BudgetUSD: raw.BudgetUSD,
		Policy: raw.Policy, Provider: raw.Provider, InstructionsFile: raw.InstructionsFile,
		AdapterConfigProvider: raw.AdapterConfigProvider, AdapterConfigMaxTokens: raw.AdapterConfigMaxTokens,
	}
	if _, err := d.UpdateRole(roleID, patch); err != nil {
		return aiError(err)
	}
	return a.saveAndRespond(root, d, "ui")
}

// PromoteRoleToRoot makes roleID the org's root role ("boss"), reversing the
// path from the current root down to roleID so the swap is correct at any
// depth — see orgdesign.Doc.PromoteToRoot. Distinct from
// SetOrgRoleReportsTo(roleID, ""), which just refuses when a different root
// already exists; this is the one-click "set as org boss" action.
func (a *App) PromoteRoleToRoot(orgName, roleID string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	if err := d.PromoteToRoot(roleID); err != nil {
		return aiError(err)
	}
	return a.saveAndRespond(root, d, "ui")
}

// ChooseInstructionsFile opens a native file picker for a role's optional
// instructions_file path (markdown or plain text). Returns the chosen
// absolute path, or "" if the user cancelled.
func (a *App) ChooseInstructionsFile() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose Instructions File",
		Filters: []runtime.FileFilter{
			{DisplayName: "Markdown", Pattern: "*.md"},
			{DisplayName: "Text", Pattern: "*.txt"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// RemoveOrgRole removes a role. strategy is "reparent" (default — direct
// reports inherit the removed role's own manager) or "cascade" (the role
// and its whole subtree).
func (a *App) RemoveOrgRole(orgName, roleID, strategy string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	s := orgdesign.RemoveStrategy(strategy)
	if s == "" {
		s = orgdesign.Reparent
	}
	removed, err := d.RemoveRole(roleID, s)
	if err != nil {
		return aiError(err)
	}
	sha, cliErr := a.saveOrgDoc(root, d, false)
	if cliErr != nil {
		return aiError(cliErr)
	}
	a.emitOrgDesignUpdated(orgName, "ui", false, d, true, nil)
	b, _ := json.Marshal(map[string]interface{}{"ok": true, "rev": sha, "deletedIds": removed})
	return string(b)
}

// SetOrgRoleReportsTo moves roleID under parentID. parentID == "" makes
// roleID the root role (only allowed when the org has no other root).
func (a *App) SetOrgRoleReportsTo(orgName, roleID, parentID string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	if err := d.SetReportsTo(roleID, parentID); err != nil {
		return aiError(err)
	}
	return a.saveAndRespond(root, d, "ui")
}

// SaveOrgLayout persists canvas positions/icons only. layoutJSON:
// {"<roleId>":{"x":120,"y":40,"icon":"coder","color":"#5b8"}, ...}. Fires on
// every drag-end, so it deliberately skips the CLI validate round-trip
// (position can't break structural validity) — see orgdesign.SetLayout.
func (a *App) SaveOrgLayout(orgName, layoutJSON string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	var pos map[string]orgdesign.RoleUI
	if err := json.Unmarshal([]byte(layoutJSON), &pos); err != nil {
		return aiError(fmt.Errorf("invalid layout: %w", err))
	}
	// Layout is role `ui`, which is not signed: no signature work here.
	_ = d.SetLayout(pos)
	sha, err := orgdesign.Save(root, d)
	if err != nil {
		return aiError(err)
	}
	if a.orgWatcher != nil {
		a.orgWatcher.MarkSelfWrite(orgName, sha)
	}
	a.emitOrgDesignUpdated(orgName, "ui", false, d, true, nil)
	b, _ := json.Marshal(map[string]interface{}{"ok": true, "rev": sha})
	return string(b)
}

// SaveOrgDesign replaces the whole document from the canvas (used for bulk
// operations like "tidy layout" or an initial import) — still runs the same
// validate/CLI-check/rollback path as every other mutator.
func (a *App) SaveOrgDesign(orgName, docJSON string) string {
	root := a.orgDesignRoot()
	var d orgdesign.Doc
	if err := json.Unmarshal([]byte(docJSON), &d); err != nil {
		return aiError(fmt.Errorf("invalid org document: %w", err))
	}
	if d.Name == "" {
		d.Name = orgName
	}
	return a.saveAndRespond(root, &d, "ui")
}

// ValidateOrgDesign checks an org's design without writing anything.
func (a *App) ValidateOrgDesign(orgName string) string {
	root := a.orgDesignRoot()
	d, err := orgdesign.Load(root, orgName)
	if err != nil {
		return aiError(err)
	}
	valid, errs := a.validateDoc(d)
	b, _ := json.Marshal(map[string]interface{}{"valid": valid, "errors": errs})
	return string(b)
}

// ReloadOrg tells a running org's daemon to pick up config changes it
// wouldn't otherwise notice (the daemon polls for a `reload` sentinel file,
// not the config's mtime). `org reload` resolves the active profile's org
// folder itself.
func (a *App) ReloadOrg(orgName string) string {
	return a.rawCLI(orgCLITimeout, "org", "reload", orgName)
}
