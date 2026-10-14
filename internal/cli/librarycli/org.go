package librarycli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/org"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// installOrg writes the library's org document into the active profile's
// org folder. A name already used needs --rename (install a copy) or --yes
// (replace it). The org arrives stopped; grants and automation roles in
// the document only survive where this profile has rows backing them, as
// with `org create-json`.
func (e *libEnv) installOrg(ctx context.Context, it *library.Item, data []byte, sum string, o libInstallOptions, res *InstallResult) error {
	var d orgdesign.Doc
	if err := json.Unmarshal(data, &d); err != nil {
		return clicore.InvalidInput("the library's org file does not parse: %v", err)
	}
	orig := d.Name
	if orig == "" {
		orig = it.Slug
	}
	name := orig
	if o.rename != "" {
		name = o.rename
	}
	if !orgdesign.ValidOrgName(name) {
		return clicore.InvalidInput("%q is not a valid org name (letters, digits, - and _; pass --rename <name>)", name)
	}
	renameOrg(&d, orig, name)
	d.Status = "stopped" // installing never starts an org
	if err := orgdesign.Validate(&d); err != nil {
		return clicore.InvalidInput("the library's org is not valid: %v", err)
	}

	env := orgenv.New(orgenv.Deps{OpenDB: e.deps.OpenDB, ProfileID: e.deps.ProfileID,
		OpenWorkflowStore: e.deps.OpenWorkflowStore, Project: func() string { return "" }})
	defer env.Close()
	db, profileID, root, err := env.Profile()
	if err != nil {
		return err
	}
	path, err := orgdesign.ConfigPath(root, name)
	if err != nil {
		return clicore.InvalidInput("%v", err)
	}
	_, statErr := os.Stat(path)
	exists := statErr == nil
	if exists && !o.yes {
		return clicore.InvalidInput("an org named %q already exists in this profile: pass --rename <new-name> to install a copy, or --yes to replace it", name)
	}
	if !exists {
		if other, err := org.NameInUse(db.DB, name, profileID); err != nil {
			return err
		} else if other != "" {
			return clicore.InvalidInput("org name %q is already used in profile %q (org names are unique on this machine): pass --rename <new-name>", name, other)
		}
	}
	res.LocalID = name
	if o.dryRun {
		res.Result = map[string]any{"org": name, "path": path, "replaces": exists, "roles": len(d.Roles)}
		return nil
	}
	if exists {
		res.Warnings = append(res.Warnings, fmt.Sprintf("replaced the existing org %q", name))
	} else if err := org.EnsureNewAutonomy(ctx, db, profileID, &d, "library"); err != nil {
		return err
	}
	rep, err := orgenv.SaveReconciled(ctx, db, profileID, root, &d, env.GenOptions(profileID))
	if err != nil {
		return err
	}
	result := map[string]any{"org": name, "path": path, "replaced": exists, "roles": len(d.Roles)}
	if rep != nil {
		result["reconcile"] = rep.Findings
	}
	res.Result, res.Installed = result, true
	return e.record(it, library.KindOrg, profileID, name, sum)
}

// renameOrg gives d a new name, carrying along the memory namespace that
// was derived from the old one.
func renameOrg(d *orgdesign.Doc, from, to string) {
	d.Name = to
	if from == to || d.RunConfig == nil {
		return
	}
	var ns string
	if json.Unmarshal(d.RunConfig["memory_namespace"], &ns) == nil && ns == "org:"+from {
		b, _ := json.Marshal("org:" + to)
		d.RunConfig["memory_namespace"] = b
	}
}
