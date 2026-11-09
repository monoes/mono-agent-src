package strategy

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
)

// The assertions live in a test file: a non-test `var _ epoch.Store` would make this package an importer of the strategy
// libraries, which the doomsday strategy imports test allows only to the named wiring files.
var (
	_ epoch.Store     = (*Store)(nil)
	_ policy.Ledger   = (*Store)(nil)
	_ policy.RunState = (*Store)(nil)
	_ epoch.RunState  = SessionRun{}
)

// The engine and the intake match the store's errors against the sstore sentinels. This package does not import sstore,
// so each of its sentinels answers to the one it stands in for.
func TestErrorsAnswerToTheSstoreSentinels(t *testing.T) {
	for mine, theirs := range map[error]error{
		ErrNotRunning:     sstore.ErrRunNotRunning,
		ErrNeedsOpenEpoch: sstore.ErrNeedsOpenEpoch,
		ErrBackwardMove:   sstore.ErrBackwardMove,
		ErrNotFound:       sstore.ErrNotFound,
		ErrConflict:       sstore.ErrConflict,
		ErrInvalid:        sstore.ErrInvalid,
		ErrUncheckedLimit: sstore.ErrUncheckedLimit,
	} {
		if !errors.Is(mine, theirs) {
			t.Errorf("%v does not answer to %v", mine, theirs)
		}
		if errors.Is(mine, sstore.ErrTampered) {
			t.Errorf("%v answers to the tamper error", mine)
		}
	}
	if errors.Is(ErrNotRunning, sstore.ErrConflict) {
		t.Error("a sentinel answers to the wrong error")
	}
}

// Only named wiring files import this package; today the read-only inputs adapter (see the want list). A new importer is decided here, in review: the store
// is not tamper-evident and must have no CLI or MCP writer of its own.
func TestOnlyTheWiringFilesImportTheMissionStrategyStore(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var got []string
	visited := 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		werr := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			visited++
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/missionpursuit/strategy/") {
				return nil
			}
			src, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, src, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, im := range f.Imports {
				if path, _ := strconv.Unquote(im.Path.Value); strings.HasSuffix(path, "/internal/missionpursuit/strategy") {
					got = append(got, rel)
					break
				}
			}
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", top, werr)
		}
	}
	if visited == 0 {
		t.Fatal("the walk visited no Go files: the guard would pass on nothing")
	}
	sort.Strings(got)
	// inputs/snapshot.go only READS (OpenDirections, ListEpochs, ParkedFinishRounds); its own guard test forbids every writer
	// and every Exec. Its test file seeds rows through the writers, which is why that file is named too; coderhost/pursuit.go builds the epoch engine and pursuit_wake.go reads its latest epoch's store over it (the pursuit loop, S4); wiring_test.go compares the store's own RunState with the pursuit one.
	want := []string{"internal/coderhost/pursuit.go", "internal/coderhost/pursuit_propose.go", "internal/coderhost/pursuit_wake.go", "internal/missionpursuit/inputs/snapshot.go", "internal/missionpursuit/inputs/snapshot_test.go", "internal/missionpursuit/inputs/wiring_test.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("importers of the mission strategy store changed:\n got %v\nwant %v\ndecide the new importer here, in review", got, want)
	}
}

// This package itself imports no authority: no vault, no tokens, no CLI, no coder host, no finish internals' state.
func TestThePackageImportsNothingThatCarriesAuthority(t *testing.T) {
	forbidden := []string{"/internal/secrets", "/internal/vault", "/internal/agenttokens", "/internal/coderhost", "/internal/cli", "/internal/mcp",
		"/internal/doomsday/store", "/internal/doomsday/stop", "/internal/doomsday/strategy/sstore", "/internal/tasks"}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			for _, bad := range forbidden {
				if strings.Contains(path, bad) {
					t.Errorf("%s imports %s", p, path)
				}
			}
			if strings.Contains(path, "internal/doomsday/strategy/") && !strings.HasSuffix(path, "/strategy/types") {
				t.Errorf("%s imports %s: only the plain types may be imported (an interface assertion belongs in a _test.go file)", p, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
