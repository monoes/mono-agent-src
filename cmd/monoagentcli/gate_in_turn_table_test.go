package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// gatedVerb is one verb routed through the shared personGate. what is the phrase the verb hands the
// gate ("only the operator can <what>"); the guard test compares the set of them with the call sites
// in internal/cli/task, so a verb gated in the source but missing here fails the build of this file's
// guard. atTerminal fakes a terminal on standard input (agents allow has its own terminal rule
// after the gate).
type gatedVerb struct {
	name       string
	args       []string
	what       string
	atTerminal bool
}

// allGatedVerbs is the one place a newly gated verb is listed for the uniform checks below.
func allGatedVerbs() []gatedVerb {
	return []gatedVerb{
		{name: "person inbox", args: []string{"session", "person", "inbox"}, what: "list the person items"},
		{name: "person full", args: []string{"session", "person", "full", "1"}, what: "answer a person item"},
		{name: "person approve", args: []string{"session", "person", "approve", "1", "--rev", "1"}, what: "answer a person item"},
		{name: "person deny", args: []string{"session", "person", "deny", "1", "--note", "x"}, what: "answer a person item"},
		{name: "person ask", args: []string{"session", "person", "ask", "1", "--text", "x"}, what: "answer a person item"},
		{name: "person answer-ask", args: []string{"session", "person", "answer-ask", "--ask", "1", "--text", "x"}, what: "answer a person item"},
		{name: "retire-worker", args: []string{"session", "retire-worker", "1", "1"}, what: "retire a session's worker"},
		{name: "edit", args: []string{"edit", "1", "--title", "x"}, what: "edit a task"},
		{name: "move", args: []string{"move", "1", "ready"}, what: "move a task"},
		{name: "archive", args: []string{"archive", "1"}, what: "archive tasks"},
		{name: "unarchive", args: []string{"unarchive", "1"}, what: "unarchive a task"},
		{name: "add --ready", args: []string{"add", "--ready", "Fix the thing"}, what: "add a task straight to Ready"},
		{name: "agents allow", args: []string{"agents", "allow", "--view"}, what: "change what AI agents may do with the Inbox", atTerminal: true},
		{name: "agents deny", args: []string{"agents", "deny", "--view"}, what: "change what AI agents may do with the Inbox"},
		{name: "os install", args: []string{"os", "install"}, what: "install the macOS menu"},
		{name: "os uninstall", args: []string{"os", "uninstall"}, what: "remove the macOS menu"},
		{name: "undepend", args: []string{"undepend", "2", "1"}, what: "remove a task's dependencies"},
	}
}

func requireOperatorOnly(t *testing.T, err error) {
	t.Helper()
	var coded interface{ JSONErrorFields() map[string]any }
	if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" {
		t.Fatalf("not refused as operator_only (exit 3): %v", err)
	}
}

// The environment layers (agent marker, workflow, chat, mission token, session token) refuse BEFORE
// any database is opened, so for every gated verb the database file must not come to exist.
func TestEveryGatedVerbIsRefusedByTheEnvironmentLayersWithoutOpeningTheDatabase(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, v := range allGatedVerbs() {
			t.Run(ctxName+"/"+v.name, func(t *testing.T) {
				cfg := sendEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
				setup(t)
				if v.atTerminal {
					atATerminal(t)
				}
				_, err := runSendCLI(t, cfg, newTaskCmd(cfg), v.args...)
				requireOperatorOnly(t, err)
				if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
					t.Errorf("the database was opened by a refused %s", ctxName)
				}
			})
		}
	}
}

// markThisProcessAsACoderTurn records this test process as the running turn of a coder session in
// the database cfg points at, the way a real coder turn's process tree is found.
func markThisProcessAsACoderTurn(t *testing.T, cfg *globalConfig) {
	t.Helper()
	d, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := coderhost.NewStore(d.DB).EnsureSession("default", "conv-lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE coder_sessions SET turn_pid = ?, turn_stamp = ? WHERE conversation_id = 'conv-lead'`, os.Getpid(), coderhost.ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
}

// Inside a coder turn (a clean operator environment, no token) the ancestry layer refuses every
// gated verb. That layer has to open the database to read the turns, so the database file existing
// is expected here and is not asserted either way: the exit code and the wording are what prove
// the ancestry layer, and not an environment layer, did the refusing.
func TestEveryGatedVerbRefusesAProcessInsideACoderTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for _, v := range allGatedVerbs() {
		t.Run(v.name, func(t *testing.T) {
			cfg := sendEnv(t)
			markThisProcessAsACoderTurn(t, cfg)
			if v.atTerminal {
				atATerminal(t)
			}
			_, err := runSendCLI(t, cfg, newTaskCmd(cfg), v.args...)
			requireOperatorOnly(t, err)
			for _, want := range []string{"coder session's turn", "only the operator can " + v.what} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refused by another layer, not the turn ancestry (missing %q): %v", want, err)
				}
			}
		})
	}
}

var (
	gateCallSite = regexp.MustCompile(`\.personGate(?:Opening)?\(cmd, ("[^"]+"|\w+)`)
	gateWhatLit  = regexp.MustCompile(`(?m)^const personWhat = ("[^"]+")`)
)

// The guard: every personGate call site in internal/cli/task names a phrase, and every phrase has a row
// in allGatedVerbs. A verb newly routed through the gate with a new phrase fails here until it is added
// to the table; a row whose phrase no call site uses (a verb ungated, or a typo) fails the other way.
// A new verb that reuses an existing phrase cannot be seen by this guard (it says what, not which verb).
func TestEveryPersonGateCallSiteHasARowInTheGatedVerbTable(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "cli", "task", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	var sources []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, string(b))
	}
	all := strings.Join(sources, "\n")
	personWhat := ""
	if m := gateWhatLit.FindStringSubmatch(all); m != nil {
		personWhat = m[1]
	}
	inSource := map[string]bool{}
	for _, m := range gateCallSite.FindAllStringSubmatch(all, -1) {
		arg := m[1]
		if arg == "personWhat" {
			arg = personWhat
		}
		if !strings.HasPrefix(arg, `"`) {
			t.Fatalf("personGate is called with %s, which this guard cannot resolve to a phrase", arg)
		}
		inSource[strings.Trim(arg, `"`)] = true
	}
	if len(inSource) == 0 {
		t.Fatal("found no personGate call sites: the guard's pattern is stale")
	}
	inTable := map[string]bool{}
	for _, v := range allGatedVerbs() {
		inTable[v.what] = true
	}
	var missing, stale []string
	for w := range inSource {
		if !inTable[w] {
			missing = append(missing, w)
		}
	}
	for w := range inTable {
		if !inSource[w] {
			stale = append(stale, w)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("allGatedVerbs and the personGate call sites disagree: gated in the source but not in the table %q; in the table but not gated in the source %q", missing, stale)
	}
}
