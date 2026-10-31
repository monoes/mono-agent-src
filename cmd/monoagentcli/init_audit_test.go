package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// The coder host's credential frame is read first thing in run() (coderhost.StashCredFrame). That ordering means
// nothing may happen before it: this test guards the part of "before" that is not in run() itself, the package
// initialisation. It scans every non-test file of every mono-agent package linked into this binary for init() bodies
// and package-level variable initialisers that do I/O (files, environment, processes, databases, network, the keychain),
// and fails on any that is not on the allowlist. It reads syntax only; third-party and standard library
// initialisation is outside its reach.

var initRiskyPkgs = map[string]bool{"os": true, "exec": true, "sql": true, "http": true, "net": true, "ioutil": true, "keyring": true, "syscall": true, "unix": true, "filepath": true}

var initRiskyFns = map[string]bool{
	"Open": true, "OpenFile": true, "ReadFile": true, "ReadDir": true, "Stat": true, "Lstat": true, "Getenv": true, "LookupEnv": true,
	"Command": true, "CommandContext": true, "Exec": true, "Query": true, "QueryRow": true, "Get": true, "Dial": true, "Listen": true, "Glob": true,
	"Mkdir": true, "MkdirAll": true, "WriteFile": true, "Create": true, "Executable": true, "UserHomeDir": true, "Getwd": true,
	"Environ": true, "Setenv": true, "Unsetenv": true, "NewFile": true, "ReadLink": true, "Readlink": true, "Remove": true,
}

// initAllowed are the initialisers allowed to read the environment before main, each with why. Both exist only in
// builds with a non-default tag.
var initAllowed = map[string]string{
	"cmd/monoagentcli/release_e2e_hook.go init(): os.Getenv":      "tag releasee2e: the release end-to-end hook, never in a shipped build",
	"internal/release/keys_e2e.go init(): os.Getenv":              "tag releasee2e: test keys for the release end-to-end run, never in a shipped build",
	"internal/operator/ancestors_devaccount.go init(): os.Getenv": "tag devaccount: development builds, never shipped",
}

// initIO lists the I/O calls in init() bodies and package-level variable initialisers (function literals are not run
// at initialisation, so their bodies are skipped) of the given source files, as "file where: pkg.Fn".
func initIO(files map[string][]byte) []string {
	var out []string
	var scan func(n ast.Node, where string)
	scan = func(n ast.Node, where string) {
		ast.Inspect(n, func(m ast.Node) bool {
			if _, isLit := m.(*ast.FuncLit); isLit {
				return false
			}
			c, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			if s, ok := c.Fun.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && initRiskyPkgs[id.Name] && initRiskyFns[s.Sel.Name] {
					out = append(out, fmt.Sprintf("%s: %s.%s", where, id.Name, s.Sel.Name))
				}
			}
			return true
		})
	}
	for name, src := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			out = append(out, name+": does not parse: "+err.Error())
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Name.Name == "init" && d.Recv == nil && d.Body != nil {
					scan(d.Body, name+" init()")
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, sp := range d.Specs {
					if v, ok := sp.(*ast.ValueSpec); ok {
						for _, val := range v.Values {
							scan(val, name+" var "+v.Names[0].Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// linkedSources returns the source files of the mono-agent packages linked into this binary under tags.
func linkedSources(t *testing.T, tags string) map[string][]byte {
	t.Helper()
	args := []string{"list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}|{{.Dir}}|{{join .GoFiles \",\"}}{{end}}"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, ".")
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	files := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 || !strings.HasPrefix(parts[0], "github.com/monoes/mono-agent") {
			continue
		}
		pkg := strings.TrimPrefix(strings.TrimPrefix(parts[0], "github.com/monoes/mono-agent"), "/")
		if pkg == "" {
			pkg = "."
		}
		for _, f := range strings.Split(parts[2], ",") {
			if f == "" {
				continue
			}
			b, err := readSource(parts[1] + "/" + f)
			if err != nil {
				t.Fatal(err)
			}
			rel := f
			if pkg != "." {
				rel = pkg + "/" + f
			}
			files[rel] = b
		}
	}
	if len(files) < 500 {
		t.Fatalf("only %d source files were found for the linked packages: the scan is not looking at the binary", len(files))
	}
	return files
}

func TestNothingDoesIOBeforeMainInTheLinkedPackages(t *testing.T) {
	for _, tags := range []string{"", "devaccount releasee2e"} {
		var unexpected []string
		for _, hit := range initIO(linkedSources(t, tags)) {
			if _, ok := initAllowed[hit]; !ok {
				unexpected = append(unexpected, hit)
			}
		}
		if len(unexpected) > 0 {
			t.Errorf("build tags %q: initialisation does I/O before main reads the credential frame:\n  %s\n(an init() or package variable must not touch files, the environment, processes, databases or the network; "+
				"if one really must, add it to initAllowed with its reason)", tags, strings.Join(unexpected, "\n  "))
		}
	}
	// the default build has none of the allowlisted ones, and the tagged build has exactly them
	if got := initIO(linkedSources(t, "")); len(got) != 0 {
		t.Errorf("the default build has initialisers that do I/O: %v", got)
	}
	tagged := initIO(linkedSources(t, "devaccount releasee2e"))
	if len(tagged) != len(initAllowed) {
		t.Errorf("the tagged build has %d I/O initialisers, the allowlist names %d: %v", len(tagged), len(initAllowed), tagged)
	}
}

// The scanner itself: an init body and a package variable that do I/O are found, a function literal and a function
// that is not init are not.
func TestTheInitScannerFindsWhatItShould(t *testing.T) {
	got := initIO(map[string][]byte{
		"a.go": []byte(`package a
import ("os"; "os/exec")
var cfg = os.Getenv("X")
var lazy = func() string { return os.Getenv("Y") }
func init() { _, _ = os.ReadFile("/etc/hosts"); exec.Command("true") }
func later() { os.Getenv("Z") }
type T struct{}
func (T) init() { os.Getenv("W") }
`),
		"b.go": []byte(`package b
import "database/sql"
var db, _ = sql.Open("sqlite3", "x")
var plain = 3
`),
	})
	// sql.Open counts: a database handle made at initialisation is a database opened before main
	want := []string{"a.go init(): exec.Command", "a.go init(): os.ReadFile", "a.go var cfg: os.Getenv", "b.go var db: sql.Open"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("found %v, want %v", got, want)
	}
}
