//go:build linux

package agentbox

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// VerifiedPackages are the package names a package directory may have to be bound into a
// strict box: the agent runner and the runtimes' own packages. A directory that merely has
// a package.json is not enough.
var VerifiedPackages = map[string]bool{
	"monomind": true, "@anthropic-ai/claude-code": true, "@openai/codex": true,
	"@google/gemini-cli": true, "opencode-ai": true, "@github/copilot": true,
}

const (
	pkgScanMaxEntries = 100000
	pkgScanMaxDepth   = 8
)

// sensitiveFile reports whether a file name looks like a credential.
func sensitiveFile(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == ".env" || strings.HasPrefix(n, ".env."), strings.HasPrefix(n, "id_"):
		return true
	case n == ".npmrc" || n == ".netrc" || n == ".pypirc" || n == ".git-credentials" || n == "credentials" || n == "secrets":
		return true
	}
	for _, sub := range []string{"credential", "secret"} {
		if strings.Contains(n, sub) && (strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".toml") || strings.HasSuffix(n, ".txt") || strings.HasSuffix(n, ".ini")) {
			return true
		}
	}
	return strings.HasSuffix(n, ".pem") || strings.HasSuffix(n, ".p12") || strings.HasSuffix(n, ".pfx")
}

// verifiedPackage reports whether dir is a package the box may bind: its package.json names
// a verified package, and nothing in the tree (outside node_modules, which is installed
// third-party code) looks like a credential or links out of the directory. A tree too big to
// scan is not verified.
func verifiedPackage(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &pj) != nil || !VerifiedPackages[pj.Name] {
		return false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	count, ok := 0, true
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return fs.SkipAll
		}
		if count++; count > pkgScanMaxEntries {
			ok = false
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if d.Name() == "node_modules" && p != root {
				return fs.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator)) >= pkgScanMaxDepth {
				ok = false
				return fs.SkipAll
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(p)
			if err != nil || (target != root && !strings.HasPrefix(target, root+"/")) {
				ok = false
				return fs.SkipAll
			}
		}
		if sensitiveFile(d.Name()) {
			ok = false
			return fs.SkipAll
		}
		return nil
	})
	return ok
}
