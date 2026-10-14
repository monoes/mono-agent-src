package workflowbundle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/workflow"
)

// NodeResolver maps a node type "<prefix>.<action>" to the installed
// package that provides that action ("" when none does, e.g. core.set or
// trigger.manual).
type NodeResolver func(prefix, action string) string

// WorkflowAutomationIDs returns the sorted, distinct package ids the
// workflow's "<prefix>.<action>" nodes use.
func WorkflowAutomationIDs(nodes []workflow.WorkflowFileNode, resolve NodeResolver) []string {
	seen := map[string]bool{}
	var ids []string
	for _, n := range nodes {
		prefix, act, ok := strings.Cut(n.Type, ".")
		if !ok || prefix == "" {
			continue
		}
		id := resolve(prefix, act)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// PackageResolver resolves (prefix, action) to the package that has the
// action. Candidates, in order:
//
//  1. the package the DefSource resolves the prefix to (the action loader's
//     view: enabled packages, legacy platform aliases such as
//     "google_maps" → local-google-maps);
//  2. an installed package with that exact id (also when disabled);
//  3. the generated legacy package for that platform
//     (Registry.ResolveLegacyPlatform, hashed ids for colliding names).
//
// The first candidate that contains the action wins, so a legacy action on a
// built-in platform (instagram.custom_scrape kept in local-instagram) is
// bundled from the legacy package, not the built-in. When no candidate has
// the action, the first candidate is returned (the node is broken either
// way; bundling the prefix's package is the closest match).
func PackageResolver(reg *automation.Registry) NodeResolver {
	src := reg.DefSource()
	actions := map[string][]string{} // package id → manifest actions
	has := func(id, act string) bool {
		list, ok := actions[id]
		if !ok {
			if p, err := reg.Get(id); err == nil {
				list = p.Manifest.Actions
			}
			actions[id] = list
		}
		for _, a := range list {
			if strings.EqualFold(a, act) {
				return true
			}
		}
		return false
	}
	return func(prefix, act string) string {
		var candidates []string
		if src != nil {
			if pc := src.Package(prefix); pc != nil {
				candidates = append(candidates, pc.ID())
			}
		}
		if info, err := reg.Info(prefix); err == nil && !info.Removed {
			candidates = append(candidates, info.ID)
		}
		if id, ok := reg.ResolveLegacyPlatform(prefix); ok {
			candidates = append(candidates, id)
		}
		for _, id := range candidates {
			if has(id, act) {
				return id
			}
		}
		if len(candidates) > 0 {
			return candidates[0]
		}
		return ""
	}
}

// CanonicalNodeTypes rewrites "<prefix>.<action>" node types to
// "<package id>.<action>" wherever the node resolved to a different,
// bundled package. A legacy alias, or a legacy action living beside a
// built-in, is a local convenience of the exporting machine — an imported
// package never claims an alias — so a bundled workflow must name the
// package it ships.
func CanonicalNodeTypes(nodes []workflow.WorkflowFileNode, resolve NodeResolver, bundled map[string]BundledAutomation) []workflow.WorkflowFileNode {
	out := make([]workflow.WorkflowFileNode, len(nodes))
	copy(out, nodes)
	for i, n := range out {
		prefix, act, ok := strings.Cut(n.Type, ".")
		if !ok || prefix == "" {
			continue
		}
		if id := resolve(prefix, act); id != "" && id != prefix {
			if _, isBundled := bundled[id]; isBundled {
				out[i].Type = id + "." + act
			}
		}
	}
	return out
}

// WriteFileAtomic writes b to path via a temp file in the same directory
// and a rename, so readers never see a partial file.
func WriteFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
