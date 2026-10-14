package automation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	tw "github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
)

// automationSession is the login block of an `automation list` row.
type automationSession struct {
	LoggedIn  bool   `json:"loggedIn"`
	Username  string `json:"username"`
	ExpiresAt string `json:"expiresAt"` // RFC3339, "" when never logged in
	Status    string `json:"status"`    // active | expired | logged_out (as `login status`)
}

// automationListRow is InstalledInfo plus the session block (contracts §5).
// PendingUpdate shadows the embedded omitempty field so it is always present.
type automationListRow struct {
	automation.InstalledInfo
	Session       automationSession `json:"session"`
	PendingUpdate string            `json:"pendingUpdate"`
}

func (d Deps) newAutomationListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed automation packages with their login state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			infos, err := reg.List(all)
			if err != nil {
				return err
			}
			sessions := d.loadAutomationSessions()
			rows := make([]automationListRow, 0, len(infos))
			for _, info := range infos {
				rows = append(rows, automationListRow{
					InstalledInfo: info,
					Session:       sessions.lookupInfo(info),
					PendingUpdate: info.PendingUpdate,
				})
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJSONTo(out, map[string]any{"automations": rows})
			}
			d.printAutomationTable(out, rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include uninstalled built-ins")
	return cmd
}

func (d Deps) printAutomationTable(out io.Writer, rows []automationListRow) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "No automations installed. Install one with: monoagentcli automation install <file.mpkg>")
		return
	}
	table := d.NewTable(out, []string{"ID", "Version", "Source", "Status", "Actions", "Login", "Notes"},
		[]tw.Align{tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignRight, tw.AlignLeft, tw.AlignLeft})
	for _, r := range rows {
		status := "enabled"
		switch {
		case r.Removed:
			status = "removed"
		case !r.Available:
			status = "unavailable"
		case !r.Enabled:
			status = "disabled"
		}
		login := "-"
		if r.Session.LoggedIn {
			login = r.Session.Username
		} else if r.Session.Username != "" {
			login = r.Session.Username + " (expired)"
		}
		var notes []string
		if r.UnavailableReason != "" {
			notes = append(notes, r.UnavailableReason)
		}
		if r.ContainsScripts {
			notes = append(notes, "scripts")
		}
		if r.Modified {
			notes = append(notes, "modified")
		}
		if r.PendingUpdate != "" {
			notes = append(notes, "update "+r.PendingUpdate+" held")
		}
		table.Append([]string{r.ID, r.Version, r.Source, status, fmt.Sprint(r.Actions), login, truncateStr(strings.Join(notes, "; "), 60)})
	}
	table.Render()
}

// sessionIndex maps a lower-cased platform to its newest crawler_sessions row.
type sessionIndex map[string]automationSession

// lookup finds id's session. A legacy-wrapped package local-<p> (the
// registry's wrap of ~/.monoagent/actions/<p>) runs on <p>'s login, so it
// falls back to <p>'s session. Prefer lookupInfo, which knows <p> exactly.
func (s sessionIndex) lookup(id string) automationSession {
	return s.lookupWith(id, "")
}

// lookupInfo is lookup for an installed package: its own id first, then
// the registry's LegacyPlatform (the original platform name, e.g.
// "google_maps", which the runtime also uses as credential_platform), then
// the id without "local-".
func (s sessionIndex) lookupInfo(info automation.InstalledInfo) automationSession {
	return s.lookupWith(info.ID, info.LegacyPlatform)
}

func (s sessionIndex) lookupWith(id, legacy string) automationSession {
	key := strings.ToLower(id)
	if v, ok := s[key]; ok {
		return v
	}
	if legacy = strings.ToLower(legacy); legacy != "" {
		if v, ok := s[legacy]; ok {
			return v
		}
	}
	if p, ok := strings.CutPrefix(key, "local-"); ok && p != "" {
		if v, ok := s[p]; ok {
			return v
		}
	}
	return automationSession{Status: "logged_out"}
}

// loadAutomationSessions reads login state from crawler_sessions (the same
// source `login status` uses), scoped to the active or --profile profile.
// It never creates a database: no DB yet means nobody has logged in.
func (d Deps) loadAutomationSessions() sessionIndex {
	idx := sessionIndex{}
	if _, err := os.Stat(d.DBPath()); err != nil {
		return idx
	}
	db, err := d.OpenDB()
	if err != nil {
		stderrf("warning: login state unavailable: %v\n", err)
		return idx
	}
	defer db.Close()
	return querySessionIndex(db.DB, d.ProfileID(), time.Now())
}

// querySessionIndex keeps, per platform, the row expiring last. The platform
// column holds both "instagram" and "INSTAGRAM" depending on the writer.
func querySessionIndex(db *sql.DB, profileID string, now time.Time) sessionIndex {
	idx := sessionIndex{}
	rows, err := db.Query(`SELECT platform, username, expiry FROM crawler_sessions WHERE profile_id = ?`, profileID)
	if err != nil {
		return idx
	}
	defer rows.Close()
	best := map[string]time.Time{}
	for rows.Next() {
		var platform, username string
		var expiry time.Time
		if rows.Scan(&platform, &username, &expiry) != nil {
			continue
		}
		key := strings.ToLower(platform)
		if prev, ok := best[key]; ok && !expiry.After(prev) {
			continue
		}
		best[key] = expiry
		status := "active"
		if expiry.Before(now) {
			status = "expired"
		}
		idx[key] = automationSession{
			LoggedIn:  status == "active",
			Username:  automation.DisplayUsername(username),
			ExpiresAt: expiry.UTC().Format(time.RFC3339),
			Status:    status,
		}
	}
	return idx
}

// automationActionJSON is one entry of `automation show --json` "actions".
type automationActionJSON struct {
	Name           string                `json:"name"`
	Description    string                `json:"description"`
	SideEffects    string                `json:"sideEffects"`
	Visibility     []string              `json:"visibility"` // what running it can reveal or leave behind; [] when nothing
	ContainsScript bool                  `json:"containsScript"`
	NodeType       string                `json:"nodeType"`
	Inputs         []automationInputJSON `json:"inputs"`
	Outputs        []string              `json:"outputs"`      // union of every outputs key, success first
	OutputsByKey   map[string][]string   `json:"outputsByKey"` // the action's outputs map as written
}

type automationInputJSON struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Default     any            `json:"default"`
	Options     any            `json:"options"`
	UI          map[string]any `json:"ui"`
}

func (d Deps) newAutomationShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show an automation's manifest, actions, inputs and outputs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			id := args[0]
			info, err := reg.Info(id)
			if err != nil {
				return err
			}
			pkg, err := reg.Get(id)
			if err != nil {
				return err
			}
			actions := describeActions(pkg)
			fragments := listFragments(pkg.FS)
			issues := automation.Validate(pkg)
			if issues == nil {
				issues = []automation.IssueJSON{}
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJSONTo(out, map[string]any{
					"info": info, "manifest": pkg.Manifest, "actions": actions,
					"fragments": fragments, "issues": issues,
					"session": d.loadAutomationSessions().lookupInfo(*info),
				})
			}
			d.printAutomationShow(out, info, pkg.Manifest, actions, fragments, issues)
			return nil
		},
	}
}

func describeActions(pkg *automation.Package) []automationActionJSON {
	out := []automationActionJSON{}
	for _, name := range pkg.Manifest.Actions {
		a := automationActionJSON{Name: name, NodeType: pkg.Manifest.ID + "." + name, Visibility: []string{},
			Inputs: []automationInputJSON{}, Outputs: []string{}, OutputsByKey: map[string][]string{}}
		def, err := pkg.Action(name)
		if err != nil {
			a.Description = "error: " + err.Error()
			out = append(out, a)
			continue
		}
		a.Description = def.Description
		a.SideEffects = def.SideEffects
		a.Visibility = append(a.Visibility, def.Visibility...)
		a.ContainsScript = stepsUseScript(def.Steps)
		if def.Inputs != nil {
			a.Inputs = append(a.Inputs, parseInputs(def.Inputs.Required, true)...)
			a.Inputs = append(a.Inputs, parseInputs(def.Inputs.Optional, false)...)
		}
		a.Outputs = unionOutputs(def.Outputs)
		for k, v := range def.Outputs {
			a.OutputsByKey[k] = append([]string{}, v...)
		}
		out = append(out, a)
	}
	return out
}

// unionOutputs flattens an outputs map: "success" first, then the other
// keys in name order, each field once. Saved/recorded actions key their
// outputs freely, so reading only "success" would lose them.
func unionOutputs(outputs map[string][]string) []string {
	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		if k != "success" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if _, ok := outputs["success"]; ok {
		keys = append([]string{"success"}, keys...)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, k := range keys {
		for _, f := range outputs[k] {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// parseInputs accepts both input forms: a bare name string, or an object
// with name/type/description/default/options/ui.
func parseInputs(raws []json.RawMessage, required bool) []automationInputJSON {
	var out []automationInputJSON
	for _, raw := range raws {
		in := automationInputJSON{Type: "string", Required: required}
		var name string
		if json.Unmarshal(raw, &name) == nil {
			in.Name = name
		} else {
			var obj struct {
				Name        string         `json:"name"`
				Type        string         `json:"type"`
				Description string         `json:"description"`
				Default     any            `json:"default"`
				Options     any            `json:"options"`
				UI          map[string]any `json:"ui"`
			}
			if json.Unmarshal(raw, &obj) != nil || obj.Name == "" {
				continue
			}
			in.Name, in.Description, in.Default, in.Options, in.UI = obj.Name, obj.Description, obj.Default, obj.Options, obj.UI
			if obj.Type != "" {
				in.Type = obj.Type
			}
		}
		if in.UI == nil {
			in.UI = map[string]any{}
		}
		out = append(out, in)
	}
	return out
}

func stepsUseScript(steps []action.StepDef) bool {
	for _, s := range steps {
		if s.Type == "page_script" || stepsUseScript(s.Steps) {
			return true
		}
	}
	return false
}

// listFragments returns fragments/<name>.json names, sorted.
func listFragments(fsys fs.FS) []string {
	out := []string{}
	if fsys == nil {
		return out
	}
	entries, _ := fs.ReadDir(fsys, "fragments")
	for _, e := range entries {
		if !e.IsDir() && path.Ext(e.Name()) == ".json" {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(out)
	return out
}

func (d Deps) printAutomationShow(out io.Writer, info *automation.InstalledInfo, m automation.Manifest,
	actions []automationActionJSON, fragments []string, issues []automation.IssueJSON) {
	fmt.Fprintf(out, "%s (%s) %s\n", m.Name, m.ID, m.Version)
	if m.Description != "" {
		fmt.Fprintf(out, "  %s\n", m.Description)
	}
	fmt.Fprintf(out, "  source: %s  trust: %s  enabled: %v  tier: %s\n", info.Source, info.Trust, info.Enabled, info.Tier)
	if !info.Available {
		fmt.Fprintf(out, "  unavailable: %s\n", info.UnavailableReason)
	}
	if len(m.Site.Domains) > 0 {
		fmt.Fprintf(out, "  domains: %s\n", strings.Join(m.Site.Domains, ", "))
	}
	if len(m.Permissions.Steps) > 0 {
		fmt.Fprintf(out, "  steps: %s\n", strings.Join(m.Permissions.Steps, ", "))
	}
	if len(m.Permissions.Scripts) > 0 {
		fmt.Fprintf(out, "  scripts: %s\n", strings.Join(m.Permissions.Scripts, ", "))
	}
	fmt.Fprintln(out)
	table := d.NewTable(out, []string{"Node type", "Effects", "Inputs", "Description"}, nil)
	for _, a := range actions {
		var ins []string
		for _, in := range a.Inputs {
			n := in.Name
			if in.Required {
				n += "*"
			}
			ins = append(ins, n)
		}
		eff := a.SideEffects
		if a.ContainsScript {
			eff += " +script"
		}
		if len(a.Visibility) > 0 {
			eff += " +visible"
		}
		table.Append([]string{a.NodeType, eff, strings.Join(ins, ", "), truncateStr(a.Description, 60)})
	}
	table.Render()
	if len(fragments) > 0 {
		fmt.Fprintf(out, "\nfragments: %s\n", strings.Join(fragments, ", "))
	}
	printIssues(out, issues)
}

// countProblems counts errors and warnings (not "info" notes).
func countProblems(issues []automation.IssueJSON) int {
	n := 0
	for _, is := range issues {
		if is.Severity == "error" || is.Severity == "warning" {
			n++
		}
	}
	return n
}

// printIssues prints validation findings, one per line.
func printIssues(out io.Writer, issues []automation.IssueJSON) {
	if len(issues) == 0 {
		return
	}
	// "info" findings (e.g. legacy_suggested_domains) are notes, not
	// problems: they are listed but not counted.
	if n := countProblems(issues); n > 0 {
		fmt.Fprintf(out, "\n%d issue(s):\n", n)
	} else {
		fmt.Fprintln(out, "\nnotes:")
	}
	for _, is := range issues {
		loc := is.File
		if is.StepID != "" {
			loc += "#" + is.StepID
		}
		fmt.Fprintf(out, "  %-7s %s %s: %s\n", is.Severity, loc, is.Code, is.Message)
	}
}
