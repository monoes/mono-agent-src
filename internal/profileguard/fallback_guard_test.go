// Package profileguard holds a test-only guard against the implicit "default"
// profile coming back (docs/mastermind/plans/2026-10-15-strict-explicit-profile.md).
package profileguard

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type rule struct {
	name string
	re   *regexp.Regexp
}

var rules = []rule{
	{"coalesce-default", regexp.MustCompile(`(?i)coalesce\(\s*(?:\w+\.)?profile_id\s*,\s*[^)]*default`)},
	{"profile-literal-default", regexp.MustCompile(`(?i)\bprofile(?:_?id)?\s*(?::=|==|=)\s*"default"`)},
	{"getenv-monoagent-profile", regexp.MustCompile(`os\.Getenv\(\s*"MONOAGENT_PROFILE"\s*\)`)},
}

// allow is the list of known remaining fallbacks. Each entry is a path (a
// file, or a directory prefix ending in /) and the rules it is excused from.
// An entry that no longer matches a hit fails the test, so the list only
// shrinks. Migrations (data/migrations/**) and *_test.go are skipped by the
// walk itself.
var allow = []struct {
	path   string
	rules  []string
	reason string
}{
	{"internal/cli/chat/coder_workspace.go", []string{defaultLiteralRule},
		"the `coder workspace default` subcommand's name (Use), not a profile"},
	{"internal/cli/connect/connect.go", []string{defaultLiteralRule},
		"a legacy profile named \"default\" gets no account/name mismatch hint; nothing falls back to it"},
	{"internal/cli/profile/names.go", []string{defaultLiteralRule},
		"refuses \"default\" as a new profile's name"},
	{"internal/nodes/nodeprofile/nodeprofile.go", []string{defaultLiteralRule},
		"ignores the legacy profile_id \"default\" saved in old workflows, keeping the run's own profile"},
	{"internal/storage/profile.go", []string{defaultLiteralRule},
		"LegacyOwnerProfile: a stored profile whose id is \"default\" owns ownerless legacy rows"},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func allowed(rel, ruleName string) (int, bool) {
	for i, a := range allow {
		match := rel == a.path || (strings.HasSuffix(a.path, "/") && strings.HasPrefix(rel, a.path))
		if !match {
			continue
		}
		for _, r := range a.rules {
			if r == ruleName {
				return i, true
			}
		}
	}
	return 0, false
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "migrations" || name == "testdata"
}

func TestNoImplicitDefaultProfile(t *testing.T) {
	root := repoRoot(t)
	used := map[int]bool{}
	var violations []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = path.Clean(filepath.ToSlash(rel))
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, r := range rules {
				if !r.re.MatchString(line) {
					continue
				}
				if i, ok := allowed(rel, r.name); ok {
					used[i] = true
					continue
				}
				violations = append(violations, fmt.Sprintf("%s:%d: %s: %s", rel, n, r.name, strings.TrimSpace(line)))
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range scanDefaultLiterals(t, root) {
		if i, ok := allowed(h.rel, h.rule); ok {
			used[i] = true
			continue
		}
		violations = append(violations, fmt.Sprintf("%s:%d: %s: %s", h.rel, h.line, h.rule, h.text))
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v)
	}
	for i, a := range allow {
		if !used[i] {
			t.Errorf("stale allow-list entry %q (%s): no hit left, delete it", a.path, a.reason)
		}
	}
}
