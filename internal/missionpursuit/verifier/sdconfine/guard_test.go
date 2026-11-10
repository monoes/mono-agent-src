package sdconfine

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func sources(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != "." {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 5 {
		t.Fatalf("only %d source files: the guard is not looking at the package", len(out))
	}
	return out
}

// The confiner runs two trusted system programs and reads a few files; it opens no connection, holds no credential and reaches
// no store.
func TestThePackageImportsNothingThatCarriesAuthority(t *testing.T) {
	forbidden := []string{"net", "net/http", "plugin", "database/sql", "/internal/storage", "/internal/secrets", "/internal/vault",
		"/internal/agenttokens", "/internal/coderhost", "/internal/cli", "/internal/mcp", "/internal/tasks", "/internal/dynorg",
		"/internal/doomsday/store", "/internal/doomsday/stop", "/internal/account", "/internal/missionpursuit/judgemodel",
		"/internal/missionpursuit/inputs", "/wails-app", "/cmd/"}
	for _, p := range sources(t) {
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			for _, bad := range forbidden {
				if path == bad || (strings.HasPrefix(bad, "/") && strings.Contains(path, bad)) {
					t.Errorf("%s imports %s", p, path)
				}
			}
			if path == "github.com/monoes/mono-agent/internal/missionpursuit" {
				t.Errorf("%s imports the pursuit store package", p)
			}
		}
	}
}

// Only systemd-run and systemctl are ever started, through the trusted-path lookup, never by a name on PATH.
func TestOnlyTheTwoTrustedBinariesAreEverStartedAndNeverByPath(t *testing.T) {
	for _, p := range sources(t) {
		body := mustRead(t, p)
		if strings.Contains(body, "exec.LookPath") {
			t.Errorf("%s resolves a program on PATH", p)
		}
		if p != "system_linux.go" && (strings.Contains(body, "exec.Command(") || strings.Contains(body, "exec.CommandContext(")) {
			t.Errorf("%s starts a process outside system_linux.go", p)
		}
		if strings.Contains(body, "\nfunc init()") {
			t.Errorf("%s declares init()", p)
		}
	}
	body := mustRead(t, "system_linux.go")
	for _, want := range []string{`"/usr/bin/systemd-run"`, `"/bin/systemd-run"`, `"/usr/bin/systemctl"`, `"/bin/systemctl"`} {
		if !strings.Contains(body, want) {
			t.Errorf("system_linux.go does not name %s", want)
		}
	}
}

func TestNoSourceFileReachesFiveHundredLines(t *testing.T) {
	for _, p := range sources(t) {
		if n := strings.Count(mustRead(t, p), "\n"); n >= 500 {
			t.Errorf("%s has %d lines", p, n)
		}
	}
}
