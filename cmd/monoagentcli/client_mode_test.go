package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/httpapi"
	"github.com/monoes/mono-agent/internal/storage"
)

const (
	cmTestToken     = "mat-clientmode-test-token-0123456789"
	cmTestTaskToken = "task-token-for-client-mode"
)

var (
	pinnedAPIMapped = []string{"aitask", "aitask cancel", "aitask complete", "aitask fail", "aitask kinds", "aitask result", "aitask status"}
	pinnedLocalSafe = []string{"help", "ref", "ref account", "ref api", "ref browser", "ref commands", "ref connections", "ref crawling",
		"ref examples", "ref expressions", "ref node", "ref nodes", "ref org", "ref publication", "ref tasks", "ref templates", "ref workflow", "version"}
)

func clientModeTree() []string {
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var keys []string
	for _, c := range allCommands(root) {
		keys = append(keys, commandKey(c))
	}
	return keys
}

// Every command path has a class for client mode, and the commands that work in it are exactly the
// pinned lists: a new command, or a new subcommand of a mapped or local-safe one, fails here until
// someone decides.
func TestEveryCommandIsClassifiedForClientMode(t *testing.T) {
	var mapped, local []string
	for _, key := range clientModeTree() {
		class, ok := clientModeClass(key)
		if !ok {
			t.Errorf("%q has no class for client mode: add it to clientModeClasses", key)
			continue
		}
		switch class {
		case cmAPIMapped:
			mapped = append(mapped, key)
		case cmLocalSafe:
			local = append(local, key)
		case cmOperatorOnly, cmRefused:
		default:
			t.Errorf("%q has class %q", key, class)
		}
	}
	for name, pair := range map[string][2][]string{"api-mapped": {pinnedAPIMapped, mapped}, "local-safe": {pinnedLocalSafe, local}} {
		want := append([]string(nil), pair[0]...)
		sort.Strings(want)
		sort.Strings(pair[1])
		if !slices.Equal(want, pair[1]) {
			t.Errorf("the %s commands differ from the pinned list\nwant %q\n got %q", name, want, pair[1])
		}
	}
}

// A table entry that names no command classifies nothing.
func TestClientModeClassesNameRealCommands(t *testing.T) {
	known := map[string]bool{}
	for _, k := range clientModeTree() {
		known[k] = true
	}
	for key, class := range clientModeClasses {
		if !known[key] {
			t.Errorf("clientModeClasses lists %q, which is not a command", key)
		}
		switch class {
		case cmAPIMapped, cmLocalSafe, cmOperatorOnly, cmRefused:
		default:
			t.Errorf("%q has the unknown class %q", key, class)
		}
	}
}

// A command added below a mapped or local-safe parent is not covered by the parent; below a
// refusal it is refused.
func TestClientModeClassDoesNotInheritCapability(t *testing.T) {
	for key, want := range map[string]string{"secret reveal": cmOperatorOnly, "workflow run now": cmRefused, "aitask budget set": cmOperatorOnly} {
		if got, ok := clientModeClass(key); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", key, got, ok, want)
		}
	}
	for _, key := range []string{"aitask brand-new", "ref brand-new", "brand-new", "help me"} {
		if class, ok := clientModeClass(key); ok {
			t.Errorf("%q got class %q from its parent", key, class)
		}
	}
}

// fakeAPI is a daemon API that records what it is asked.
type fakeAPI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
	// reply decides the answer; the default is a running task.
	reply func(r *http.Request, body []byte) (int, string)
}

type recorded struct {
	method, path, auth, taskToken, body string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Aitask-Token"), string(b)})
		f.mu.Unlock()
		status, body := http.StatusOK, `{"taskId":"at-1","kind":"generic","conversationId":"c1","status":"running","goal":"g"}`
		if f.reply != nil {
			status, body = f.reply(r, b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// clientEnv switches this process to client mode against base and returns a database path that does
// not exist, and the fresh HOME.
func clientEnv(t *testing.T, base string) (dbPath, home string) {
	t.Helper()
	home = freshHome(t)
	t.Setenv("MONOAGENT_API_URL", base+"/profiles/p1")
	t.Setenv("MONOAGENT_API_TOKEN", cmTestToken)
	t.Setenv("MONOAGENT_AITASK_TOKEN", cmTestTaskToken)
	return filepath.Join(home, "nowhere", "monoagent.db"), home
}

func assertNothingLocal(t *testing.T, dbPath, home string) {
	t.Helper()
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("the local database path was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(err) {
		t.Errorf("the local database folder was made: %v", err)
	}
	if files := filesUnder(t, home); len(files) > 0 {
		t.Errorf("the command wrote %v to HOME", files)
	}
}

func assertNoToken(t *testing.T, outs ...string) {
	t.Helper()
	for _, o := range outs {
		if strings.Contains(o, cmTestToken) || strings.Contains(o, cmTestTaskToken) {
			t.Errorf("a token was printed: %q", o)
		}
	}
}

func TestClientModeMappedCommandsGoToTheAPIAndNeverOpenTheDatabase(t *testing.T) {
	api := newFakeAPI(t)
	db, home := clientEnv(t, api.srv.URL)
	result := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(result, []byte(`{"summary":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A result file is refused (the task cannot be looked up to tell whether it is confined); nothing is sent.
	if code, out, _ := runMain(t, "aitask", "complete", "at-1", "--result", result, "--db-path", db, "--profile", "p1", "--json"); code != 3 ||
		!strings.Contains(out, "--result-json") || len(api.requests()) != 0 {
		t.Errorf("a result file in client mode: exit %d %q, %d requests", code, out, len(api.requests()))
	}
	g := []string{"--db-path", db, "--profile", "p1", "--json"}
	cases := []struct {
		args         []string
		method, path string
		taskToken    string
		body         string
	}{
		{[]string{"aitask", "status", "at-1"}, "GET", "/profiles/p1/agent/v1/aitask/at-1", "", ""},
		{[]string{"aitask", "result", "at-1"}, "GET", "/profiles/p1/agent/v1/aitask/at-1/result", "", ""},
		{[]string{"aitask", "complete", "at-1", "--result-json", `{"summary":"done"}`}, "POST", "/profiles/p1/agent/v1/aitask/at-1/complete", cmTestTaskToken, `{"summary":"done"}`},
		{[]string{"aitask", "fail", "at-1", "--reason", "cannot"}, "POST", "/profiles/p1/agent/v1/aitask/at-1/fail", cmTestTaskToken, `{"reason":"cannot"}`},
		{[]string{"aitask", "cancel", "at-1"}, "POST", "/profiles/p1/agent/v1/aitask/at-1/cancel", cmTestTaskToken, ""},
		{[]string{"aitask", "kinds"}, "GET", "/profiles/p1/agent/v1/aitask/kinds", "", ""},
	}
	for _, c := range cases {
		before := len(api.requests())
		code, out, errOut := runMain(t, append(append([]string{}, c.args...), g...)...)
		reqs := api.requests()
		if len(reqs) != before+1 {
			t.Fatalf("%v: %d requests, want one more than %d (exit %d, %s %s)", c.args, len(reqs), before, code, out, errOut)
		}
		got := reqs[before]
		if code != 0 || got.method != c.method || got.path != c.path || got.auth != "Bearer "+cmTestToken || got.taskToken != c.taskToken || got.body != c.body {
			t.Errorf("%v: exit %d, request %+v (stderr %q)", c.args, code, got, errOut)
		}
		assertNoToken(t, out, errOut)
		assertNothingLocal(t, db, home)
	}
	// Human output too.
	code, out, _ := runMain(t, "aitask", "status", "at-1", "--db-path", db, "--profile", "p1")
	if code != 0 || !strings.Contains(out, "at-1") || strings.Contains(out, "{") {
		t.Errorf("human status: %d %q", code, out)
	}
}

// The refusals of the API keep the codes and exit codes of the command path, and no token
// the API echoed survives.
func TestClientModeRefusalsKeepTheirCodes(t *testing.T) {
	api := newFakeAPI(t)
	api.reply = func(r *http.Request, _ []byte) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/complete"):
			return 422, `{"error":"the result does not match the generic schema (` + cmTestToken + `)","code":"schema_invalid","reason":"summary is required"}`
		case strings.HasSuffix(r.URL.Path, "/fail"):
			return 403, `{"error":"no"}`
		}
		return 404, `{"error":"no AI task at-9","code":"not_found"}`
	}
	db, home := clientEnv(t, api.srv.URL)
	g := []string{"--db-path", db, "--profile", "p1", "--json"}

	code, out, errOut := runMain(t, append([]string{"aitask", "complete", "at-1", "--result-json", `{}`}, g...)...)
	var doc map[string]any
	if code != 3 || json.Unmarshal([]byte(strings.TrimSpace(out)), &doc) != nil || doc["code"] != "schema_invalid" || doc["reason"] != "summary is required" {
		t.Errorf("complete: exit %d %q", code, out)
	}
	assertNoToken(t, out, errOut)
	if code, out, _ = runMain(t, append([]string{"aitask", "status", "at-9"}, g...)...); code != 2 || !strings.Contains(out, `"not_found"`) {
		t.Errorf("status of nothing: exit %d %q", code, out)
	}
	if code, out, errOut = runMain(t, append([]string{"aitask", "fail", "at-1", "--reason", "x"}, g...)...); code != 3 || !strings.Contains(out, "agent_api_refused") {
		t.Errorf("a refused token: exit %d %q", code, out)
	}
	assertNoToken(t, out, errOut)
	assertNothingLocal(t, db, home)
}

// A daemon that cannot be reached is an error, never a reason to open the local database.
func TestClientModeUnreachableAPIDoesNotFallBack(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	base := dead.URL
	dead.Close()
	db, home := clientEnv(t, base)
	code, out, errOut := runMain(t, "aitask", "status", "at-1", "--db-path", db, "--profile", "p1", "--json")
	if code == 0 || !strings.Contains(out, "agent_api_unavailable") {
		t.Errorf("exit %d %q %q", code, out, errOut)
	}
	assertNoToken(t, out, errOut)
	assertNothingLocal(t, db, home)
}

// Everything else is refused with exit 3 and the code, nothing is forwarded and nothing is opened.
func TestClientModeRefusesEveryOtherCommand(t *testing.T) {
	api := newFakeAPI(t)
	db, home := clientEnv(t, api.srv.URL)
	for _, args := range [][]string{
		{"aitask", "submit", "--kind", "generic", "--goal", "x"}, // an operator check is not passed by client mode
		{"aitask", "start", "at-1"},
		{"aitask", "list"},
		{"aitask", "budget", "set", "--scope", "global", "--usd", "1"},
		{"agent-mode", "token", "create", "--name", "x", "--scope", "run"},
		{"secret", "list"},
		{"workflow", "list"},
		{"task", "list"},
		{"people", "list"},
		{"daemon", "status"},
		{"doctor"},
		{"completion", "bash"},
		{"__complete", "aitask", ""},
		{"no-such-command"},
	} {
		for _, asJSON := range []bool{false, true} {
			full := append(append([]string{}, args...), "--db-path", db, "--profile", "p1")
			if asJSON {
				full = append(full, "--json")
			}
			code, out, errOut := runMain(t, full...)
			if args[0] == "no-such-command" {
				if code == 0 {
					t.Errorf("%v: exit 0", args)
				}
				continue
			}
			if code != 3 || !strings.Contains(errOut, "not available in agent mode") {
				t.Errorf("%v json=%v: exit %d stdout %q stderr %q", args, asJSON, code, out, errOut)
			}
			if asJSON && !strings.Contains(out, `"code":"`+codeNotAvailableInAgentMode+`"`) {
				t.Errorf("%v: json %q", args, out)
			}
			assertNoToken(t, out, errOut)
		}
	}
	if n := len(api.requests()); n != 0 {
		t.Errorf("%d requests were forwarded", n)
	}
	assertNothingLocal(t, db, home)
}

// Commands that read embedded text only run as ever, and open nothing.
func TestClientModeLocalSafeCommands(t *testing.T) {
	api := newFakeAPI(t)
	db, home := clientEnv(t, api.srv.URL)
	for _, args := range [][]string{{"version"}, {"help"}, {"help", "aitask"}, {"aitask", "--help"}, {"secret", "--help"}, {}} {
		code, out, errOut := runMain(t, append(append([]string{}, args...), "--db-path", db)...)
		if code != 0 || (out+errOut == "" && args[0] != "version") { // version prints on the real stdout
			t.Errorf("%v: exit %d %q %q", args, code, out, errOut)
		}
		assertNoToken(t, out, errOut)
	}
	if n := len(api.requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
	assertNothingLocal(t, db, home)
}

// Client mode needs both variables; one alone changes nothing.
func TestClientModeNeedsBothVariables(t *testing.T) {
	for _, set := range [][2]string{{"MONOAGENT_API_URL", "http://127.0.0.1:1"}, {"MONOAGENT_API_TOKEN", cmTestToken}} {
		t.Setenv("MONOAGENT_API_URL", "")
		t.Setenv("MONOAGENT_API_TOKEN", "")
		t.Setenv(set[0], set[1])
		if inClientMode() {
			t.Errorf("%s alone turned client mode on", set[0])
		}
	}
}

// End to end against the real API handlers: the same command, a database only the daemon has.
func TestClientModeCompleteAgainstTheRealAPI(t *testing.T) {
	daemonDB := filepath.Join(t.TempDir(), "daemon.db")
	db, err := storage.NewDatabase(daemonDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	store := &aitask.Store{DB: db.DB, Profile: "default"}
	if _, err := store.Insert(aitask.Task{ID: "at-1", Kind: "generic", ConversationID: "c1", Status: aitask.StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetToken("at-1", cmTestTaskToken); err != nil {
		t.Fatal(err)
	}
	_, secret, err := agenttokens.NewStore(db.DB).CreateBound(context.Background(), "default", "turn", agenttokens.ScopeWrite, time.Hour,
		agenttokens.Binding{TaskID: "at-1", ConversationID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	srv, err := httpapi.NewServer(httpapi.Options{DBPath: daemonDB, WorkflowsDir: t.TempDir(), Addr: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	agentDB, home := clientEnv(t, ts.URL)
	t.Setenv("MONOAGENT_API_URL", ts.URL+"/profiles/default")
	t.Setenv("MONOAGENT_API_TOKEN", secret)
	g := []string{"--db-path", agentDB, "--profile", "default", "--json"}

	if code, out, _ := runMain(t, append([]string{"aitask", "complete", "at-1", "--result-json", `{"nope":1}`}, g...)...); code != 3 || !strings.Contains(out, "schema_invalid") {
		t.Fatalf("a bad result: exit %d %q", code, out)
	}
	code, out, errOut := runMain(t, append([]string{"aitask", "complete", "at-1", "--result-json", `{"summary":"all done"}`}, g...)...)
	if code != 0 || !strings.Contains(out, `"complete"`) {
		t.Fatalf("complete: exit %d %q %q", code, out, errOut)
	}
	if code, out, _ = runMain(t, append([]string{"aitask", "complete", "at-1", "--result-json", `{"summary":"all done"}`}, g...)...); code != 3 || !strings.Contains(out, "task_closed") {
		t.Fatalf("second complete: exit %d %q", code, out)
	}
	if code, out, _ = runMain(t, append([]string{"aitask", "result", "at-1"}, g...)...); code != 0 || !strings.Contains(out, "all done") {
		t.Fatalf("result: exit %d %q", code, out)
	}
	if code, out, _ = runMain(t, append([]string{"aitask", "kinds"}, g...)...); code != 0 || !strings.Contains(out, `"generic"`) {
		t.Fatalf("kinds: exit %d %q", code, out)
	}
	assertNoToken(t, out)
	if _, err := os.Stat(agentDB); !os.IsNotExist(err) {
		t.Errorf("the agent side opened a database: %v", err)
	}
	_ = home
}

// Beneath the table, the two ways the CLI reaches the local database refuse in client mode.
func TestClientModeBacksTheTableWithTheDatabaseOpeners(t *testing.T) {
	db, home := clientEnv(t, "http://127.0.0.1:1")
	if d, err := initDB(&globalConfig{DBPath: db}); err == nil || d != nil {
		t.Errorf("initDB opened the database in client mode: %v", err)
	}
	if _, err := listProfilesReadOnly(db); err == nil {
		t.Error("the profile list was read in client mode")
	}
	assertNothingLocal(t, db, home)
}

// No log file is opened in client mode, and a locally generated error that repeats an argument
// never prints either credential.
func TestClientModeScrubsOutputAndOpensNoLogFile(t *testing.T) {
	api := newFakeAPI(t)
	db, home := clientEnv(t, api.srv.URL)
	logFile := filepath.Join(home, "agent.log")
	code, out, errOut := runMain(t, "aitask", "status", "at-1", "--profile", "p1", "--db-path", db, "--log-file", logFile)
	if code != 3 || !strings.Contains(errOut, "not available in agent mode") {
		t.Errorf("--log-file: exit %d %q", code, errOut)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("a log file was opened: %v", err)
	}
	assertNoToken(t, out, errOut)
	for _, v := range []string{cmTestToken, cmTestTaskToken} {
		for _, asJSON := range []bool{false, true} {
			args := []string{"aitask", "status", "at-1", "--profile", "p1", "--db-path", db, "--workers", v}
			if asJSON {
				args = append(args, "--json")
			}
			code, out, errOut = runMain(t, args...)
			if code == 0 || out+errOut == "" {
				t.Errorf("bad --workers: exit %d %q %q", code, out, errOut)
			}
			assertNoToken(t, out, errOut)
		}
	}
	assertNothingLocal(t, db, home)
}
