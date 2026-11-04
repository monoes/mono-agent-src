package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The database doctrine (board #76): the CLI owns the database. It is the one
// writer and the one that migrates (`db migrate`, app_db_prepare.go), and the
// app's own handle is read-only (app_db_readonly.go). A binding that needs
// data shells out to `monoagentcli … --json`. This test keeps it that way for
// every non-test Go file of the app, so a new binding cannot reach the
// database through the storage package, run a migration, or open a handle
// of its own.

// dbForbiddenImports may not be imported by any app file.
var dbForbiddenImports = []string{
	"github.com/monoes/mono-agent/internal/storage",
}

// dbAllowedReaders are the packages that read the database through the app's
// read-only handle, and the only files that may import them, each for the
// reason given. Adding a file here is a decision: prefer a CLI command.
var dbAllowedReaders = map[string]map[string]string{
	"github.com/monoes/mono-agent/internal/tasks": {
		// A two-second board-revision poll; a CLI process per tick would be
		// far heavier than one primary-key read.
		"app_tasks_watch.go": "task board revision watcher and TaskPulse",
		// `task board` refuses an agent-driven caller; the window shows the
		// operator's board read-only whatever started the app.
		"app_tasks.go": "TaskBoard snapshot as the operator",
	},
	"github.com/monoes/mono-agent/internal/profiledir": {
		// profiles.root_dir, read on every org binding, tab load and watcher
		// restart; documents need it byte-identical to monomind's own read.
		"app.go":                 "org watcher directory, monograph bootstrap folder",
		"app_orgs.go":            "org project root (mkdir of the layout, no database write)",
		"app_documents_watch.go": "document watcher root",
		"app_monomind_init.go":   "IsMonomindInitialized folder check",
		// ValidProfileID is a pure syntax check; the signed-out Stop reads
		// the state file, never the database.
		"profile_selection.go": "profile id syntax check for the persisted-profile Stop",
	},
	"github.com/monoes/mono-agent/internal/vault": {
		// One lookup per <img> request of the vault image handler.
		"main.go": "vault image path for the asset handler",
	},
}

// dbForbiddenCalls are function names no app file may call: the migrations
// belong to `db migrate`.
var dbForbiddenCalls = map[string]bool{
	"ApplyMigrations":              true,
	"MigrateVaultFiles":            true,
	"MigrateVaultDirIntoMonoagent": true,
	"MigrateProfileVaultKeys":      true,
	"MigrateProfileBlobs":          true,
	"MigrateConnectionsToVault":    true,
	"MigrateFieldsToKV":            true,
	"MigrateSessionsToVault":       true,
	"RetireAIProviderEntries":      true,
	"NewDatabase":                  true,
}

// sqlOpenFile is the one file that opens the database, read-only.
const sqlOpenFile = "app_db_readonly.go"

func TestAppDatabaseDoctrine(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seenReaders := map[string]map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range dbForbiddenImports {
				if path == bad {
					t.Errorf("%s imports %s: the app reaches data through `monoagentcli … --json`, not the storage package", name, path)
				}
			}
			if allowed, ok := dbAllowedReaders[path]; ok {
				if _, ok := allowed[name]; !ok {
					t.Errorf("%s imports %s, which reads the database: shell out to the CLI, or add the file to dbAllowedReaders with its reason", name, path)
				}
				if seenReaders[path] == nil {
					seenReaders[path] = map[string]bool{}
				}
				seenReaders[path][name] = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch c := call.Fun.(type) {
			case *ast.SelectorExpr:
				fn = c.Sel.Name
				if x, ok := c.X.(*ast.Ident); ok && x.Name == "sql" && fn == "Open" && name != sqlOpenFile {
					t.Errorf("%s: sql.Open outside %s: the app has one read-only handle", fset.Position(call.Pos()), sqlOpenFile)
				}
			case *ast.Ident:
				fn = c.Name
			}
			if dbForbiddenCalls[fn] {
				t.Errorf("%s calls %s: migrations run in the CLI (`monoagentcli db migrate`)", fset.Position(call.Pos()), fn)
			}
			return true
		})
	}

	// A stale allowlist entry would quietly let that file start reading again.
	var stale []string
	for path, allowed := range dbAllowedReaders {
		for file := range allowed {
			if !seenReaders[path][file] {
				stale = append(stale, file+" → "+path)
			}
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("dbAllowedReaders lists %s, which no longer imports it: remove the entry", s)
	}

	src, err := os.ReadFile(sqlOpenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "_pragma=query_only(1)") {
		t.Errorf("%s no longer opens the database with query_only", sqlOpenFile)
	}
}
