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
// a package.json is not enough, and even a verified one is bound only through its manifest.
var VerifiedPackages = map[string]bool{
	"monomind": true, "@anthropic-ai/claude-code": true, "@openai/codex": true,
	"@google/gemini-cli": true, "opencode-ai": true, "@github/copilot": true,
}

const (
	pkgScanMaxEntries = 100000
	pkgScanMaxDepth   = 8
)

// sensitiveFile reports whether a file name looks like a credential. It is a second line of
// defence for the manifest entries that are scanned; what is bound is decided by the manifest.
func sensitiveFile(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == ".env" || strings.HasPrefix(n, ".env."), strings.HasPrefix(n, "id_"):
		return true
	case n == ".npmrc" || n == ".netrc" || n == ".pypirc" || n == ".git-credentials" || n == "credentials" || n == "secrets",
		n == "auth.json" || n == "token.json" || n == "tokens.json" || strings.HasPrefix(n, "oauth"):
		return true
	}
	for _, sub := range []string{"credential", "secret"} {
		if strings.Contains(n, sub) && (strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".toml") || strings.HasSuffix(n, ".txt") || strings.HasSuffix(n, ".ini")) {
			return true
		}
	}
	return strings.HasSuffix(n, ".pem") || strings.HasSuffix(n, ".p12") || strings.HasSuffix(n, ".pfx")
}

// packageEntryNames reads the package.json inside dirPath (a path naming the pinned
// directory) and, if it names a verified package, returns the top-level entries to bind: the
// fixed manifest plus the first path component of main and bin.
func packageEntryNames(dirPath string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(dirPath, "package.json"))
	if err != nil {
		return nil, false
	}
	var pj struct {
		Name string          `json:"name"`
		Main string          `json:"main"`
		Bin  json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(data, &pj) != nil || !VerifiedPackages[pj.Name] {
		return nil, false
	}
	set := map[string]bool{}
	var names []string
	add := func(p string) {
		first := strings.SplitN(filepath.Clean(strings.TrimPrefix(p, "./")), string(filepath.Separator), 2)[0]
		if first != "" && first != "." && first != ".." && !strings.HasPrefix(first, "/") && !set[first] && !sensitiveFile(first) {
			set[first] = true
			names = append(names, first)
		}
	}
	for _, n := range packageManifest {
		add(n)
	}
	add(pj.Main)
	var one string
	var many map[string]string
	if json.Unmarshal(pj.Bin, &one) == nil {
		add(one)
	} else if json.Unmarshal(pj.Bin, &many) == nil {
		for _, v := range many {
			add(v)
		}
	}
	return names, true
}

// entryClean scans one bound entry (a path naming the pinned entry) for credential-like file
// names and for links that leave realRoot. A tree too big or too deep to scan is not clean.
func entryClean(entryPath, realRoot string) bool {
	if fi, err := os.Stat(entryPath); err == nil && fi.IsDir() {
		entryPath += "/" // follow the descriptor's link into the directory
	}
	count, ok := 0, true
	_ = filepath.WalkDir(entryPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return fs.SkipAll
		}
		if count++; count > pkgScanMaxEntries {
			ok = false
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(entryPath, p)
		if d.IsDir() {
			if d.Name() == "node_modules" && p != entryPath {
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
			if err != nil || (target != realRoot && !strings.HasPrefix(target, realRoot+"/")) {
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
