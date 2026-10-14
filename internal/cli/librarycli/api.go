package librarycli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/library"
)

// apiIDRe is the apipkg.v1 id pattern; an id names its install folder, so
// nothing else may reach the file system.
var apiIDRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// APIInstallResult is the `result` of installing an API package.
type APIInstallResult struct {
	ID         string `json:"id"`
	NodeType   string `json:"node_type,omitempty"`
	Extends    string `json:"extends,omitempty"`
	Path       string `json:"path"`
	Operations int    `json:"operations"`
	Replaces   bool   `json:"replaces"`
}

// apiPackagePath is ~/.monoagent/apipkgs/<id>/package.json for a valid id.
func apiPackagePath(id string) (string, error) {
	if !apiIDRe.MatchString(id) {
		return "", fmt.Errorf("%q is not a valid API package id (a-z, 0-9 and _ only)", id)
	}
	return filepath.Join(apipkg.InstallDir(), id, "package.json"), nil
}

func apiPackageInstalled(id string) bool {
	path, err := apiPackagePath(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// readAPIPackage reads what `library publish api <local>` uploads: an
// installed package by id, else a package.json file or a folder holding one.
// It is validated as the loader would; built-in packages have no file.
func readAPIPackage(local string) ([]byte, *apipkg.Package, error) {
	path := local
	if apiPackageInstalled(local) {
		path, _ = apiPackagePath(local)
	} else if st, err := os.Stat(local); err == nil && st.IsDir() {
		path = filepath.Join(local, "package.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		if p, ok := apipkg.ByID(local); ok && p.Source == "catalog" {
			return nil, nil, clicore.InvalidInput("%q is a built-in API package; to publish a changed copy, install it as ~/.monoagent/apipkgs/%s/package.json or pass its file", local, local)
		}
		return nil, nil, clicore.NotFound("no installed API package %q and no file %s", local, path)
	}
	p, err := apipkg.Parse(b, path)
	if err != nil {
		return nil, nil, clicore.InvalidInput("%v", err)
	}
	return b, p, nil
}

// apiRecord is the provenance record of the installed package id (API
// packages are shared by every profile, as automations are).
func (e *libEnv) apiRecord(id string) (library.Record, bool) {
	prov, err := e.provenance()
	if err != nil {
		return library.Record{}, false
	}
	recs, _ := prov.List(e.deps.ProfileID(), library.KindAPI)
	for _, r := range recs {
		if r.LocalID == id && r.Profile == "" {
			return r, true
		}
	}
	return library.Record{}, false
}

// installAPI validates the library's API package and writes it to
// ~/.monoagent/apipkgs/<id>/package.json. It may replace a built-in package
// of the same id (as any installed package does); replacing an installed
// package that came from elsewhere needs --yes.
func (e *libEnv) installAPI(it *library.Item, data []byte, sum string, o libInstallOptions, res *InstallResult) error {
	p, err := apipkg.Parse(data, "the library's API package")
	if err != nil {
		return clicore.InvalidInput("%v — nothing was installed", err)
	}
	path, err := apiPackagePath(p.ID)
	if err != nil {
		return clicore.InvalidInput("%v — nothing was installed", err)
	}
	exists := apiPackageInstalled(p.ID)
	if exists && !o.yes {
		if r, ok := e.apiRecord(p.ID); !ok || r.ItemID != it.ID {
			return clicore.InvalidInput("an API package %q is already installed (%s) and did not come from this item: pass --yes to replace it", p.ID, path)
		}
	}
	// The loader keeps one package per node type: a second installed one
	// would be left out, so say so now instead.
	pkgs, _ := apipkg.Load()
	for _, q := range pkgs {
		if q.ID != p.ID && q.Source != "catalog" && q.TargetType() == p.TargetType() {
			return clicore.InvalidInput("the installed API package %q already provides %s, so %q would not load — nothing was installed", q.ID, p.TargetType(), p.ID)
		}
	}
	result := &APIInstallResult{ID: p.ID, NodeType: p.NodeTypeOf(), Extends: p.Extends, Path: path,
		Operations: len(p.Operations), Replaces: exists}
	res.LocalID, res.Result = p.ID, result
	if o.dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := workflowbundle.WriteFileAtomic(path, data); err != nil {
		return err
	}
	apipkg.Reload()
	if got, ok := apipkg.ByID(p.ID); !ok || got.Source != path {
		_, loadErr := apipkg.Load()
		res.Warnings = append(res.Warnings, fmt.Sprintf("installed, but the package did not load: %v", loadErr))
	}
	if exists {
		res.Warnings = append(res.Warnings, fmt.Sprintf("replaced the installed API package %q", p.ID))
	}
	res.Installed = true
	return e.record(it, library.KindAPI, "", p.ID, sum)
}
