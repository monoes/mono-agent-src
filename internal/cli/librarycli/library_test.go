package librarycli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent, the OS keychain or monoes.me: the library
// is libraryfake on 127.0.0.1, and the machine session is signed at it.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// errLogin stands in for the CLI's "log in to monoes.me first".
var errLogin = &clicore.Error{Code: 4, Msg: library.ErrNotLoggedIn.Error()}

// fixture is the CLI as the commands see it: a migrated database, a registry of its
// own, a fake monoes.me and a sign-in that follows the fake's redirect.
type fixture struct {
	t      *testing.T
	fake   *libraryfake.Server
	dbPath string
	reg    *automation.Registry
	signed int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := libraryfake.New()
	t.Cleanup(fake.Close)
	t.Setenv("MONOES_BASE_URL", fake.URL)
	account.SetHostForTest(t, fake.URL)
	account.SetSealerForTest(t, account.NewMemorySealer())
	libraryfake.TrustKey(t)
	dbPath := filepath.Join(t.TempDir(), "lib.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reg, err := automation.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = account.Logout(t.Context()) })
	return &fixture{t: t, fake: fake, dbPath: dbPath, reg: reg}
}

func (f *fixture) deps(jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		ProfileID:  func() string { return "" },
		OpenDB: func() (*storage.Database, error) {
			return storage.NewDatabase(f.dbPath)
		},
		OpenRegistry:   func() (*automation.Registry, error) { return f.reg, nil },
		WithJSONErrors: func(*cobra.Command) {},
		AddSignIn: func(cmd *cobra.Command, name string) func(*cobra.Command) (account.Status, bool, error) {
			cmd.Flags().Bool("no-browser", false, "")
			return func(cmd *cobra.Command) (account.Status, bool, error) {
				f.signed++
				st, err := account.Login(cmd.Context(), account.LoginOptions{Open: func(u string) error {
					go func() {
						if resp, err := http.Get(u); err == nil {
							resp.Body.Close()
						}
					}()
					return nil
				}})
				return st, false, err
			}
		},
		NewWorkflowImportJSONCmd: func() *cobra.Command { panic("not used") },
		LoginRequired:            func() error { return errLogin },
		IsLoginRequired: func(err error) bool {
			return errors.Is(err, library.ErrNotLoggedIn) || errors.Is(err, errLogin) || account.IsLoginRequired(err)
		},
	}
}

// run runs `library <args>` and returns stdout and the error.
func (f *fixture) run(jsonOut bool, args ...string) (string, error) {
	f.t.Helper()
	cmd := NewCommand(f.deps(jsonOut))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.ExecuteContext(f.t.Context())
	return out.String(), err
}

func (f *fixture) must(v any, args ...string) string {
	f.t.Helper()
	out, err := f.run(v != nil, args...)
	if err != nil {
		f.t.Fatalf("%v: %v\n%s", args, err, out)
	}
	if v != nil {
		if err := json.Unmarshal([]byte(out), v); err != nil {
			f.t.Fatalf("%v: not JSON: %v\n%s", args, err, out)
		}
	}
	return out
}

func pack(t *testing.T, id string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := automation.Pack(filepath.Join("..", "..", "..", "automations", id), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGroupHasEverySubcommand(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(Deps{WithJSONErrors: func(*cobra.Command) {}, AddSignIn: func(*cobra.Command, string) func(*cobra.Command) (account.Status, bool, error) {
		return nil
	}})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	want := "install installed list login logout publish show status update"
	if strings.Join(got, " ") != want {
		t.Fatalf("subcommands = %v, want %s", got, want)
	}
}

// Every library failure gets the CLI's exit code.
func TestErrExitCodes(t *testing.T) {
	t.Parallel()
	d := Deps{LoginRequired: func() error { return errLogin }}
	for name, c := range map[string]struct {
		err  error
		code int
	}{
		"not logged in": {library.ErrNotLoggedIn, 4},
		"404":           {&library.APIError{Status: http.StatusNotFound, Message: "gone"}, 2},
		"400":           {&library.APIError{Status: http.StatusBadRequest}, 3},
		"409":           {&library.APIError{Status: http.StatusConflict}, 3},
		"413":           {&library.APIError{Status: http.StatusRequestEntityTooLarge}, 3},
		"422":           {&library.APIError{Status: http.StatusUnprocessableEntity}, 3},
		"403":           {&library.APIError{Status: http.StatusForbidden}, 4},
		"429":           {&library.APIError{Status: http.StatusTooManyRequests}, 4},
		"502":           {&library.APIError{Status: http.StatusBadGateway}, 4},
		"sha256":        {library.ErrSHA256Mismatch, 3},
		"unreachable":   {errors.New("monoes.me unreachable: dial tcp"), 4},
		"already coded": {clicore.NotFound("nope"), 2},
		"other":         {errors.New("boom"), 1},
	} {
		if got := clicore.ExitCodeFor(d.Err(c.err)); got != c.code {
			t.Errorf("%s: exit %d, want %d", name, got, c.code)
		}
	}
	if d.Err(library.ErrNotLoggedIn) != errLogin || d.Err(nil) != nil {
		t.Error("login required or nil not passed through")
	}
	if err := d.Err(automation.ErrSHA256Mismatch); !strings.HasSuffix(err.Error(), " — nothing was installed") {
		t.Errorf("sha256 mismatch message = %q", err)
	}
}

func TestVersionsNamesAndHashes(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"1.2.3": "1.2.4", "1.2.9-beta+x": "1.2.10", "": "1.0.0", "2": "2.1", "1.2.x": "1.2.x.1"} {
		if got := bumpVersion(in); got != want {
			t.Errorf("bumpVersion(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"My Workflow!": "my-workflow", "  ": "workflow", "Ä b--c": "b-c"} {
		if got := slugFile(in); got != want {
			t.Errorf("slugFile(%q) = %q, want %q", in, got, want)
		}
	}
	it := &library.Item{Version: "1.1.0", SHA256: "AA"}
	if !newerThan(it, "1.0.0", "") || newerThan(it, "1.2.0", "") || newerThan(it, "1.1.0", "aa") || !newerThan(it, "1.1.0", "bb") {
		t.Error("newerThan")
	}
	if shortHash("0123456789abcdef") != "0123456789ab" || shortHash("ab") != "ab" {
		t.Error("shortHash")
	}
	if got := mergeSorted([]string{"b", "a"}, []string{"a", "c"}); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("mergeSorted = %v", got)
	}
	if got := string(lastJSONObject([]byte("note\n{\"a\":1}\n"))); got != `{"a":1}` {
		t.Errorf("lastJSONObject = %s", got)
	}
}

// An org installed under another name carries its derived memory namespace along.
func TestRenameOrgCarriesTheNamespace(t *testing.T) {
	t.Parallel()
	d := orgdesign.Doc{Name: "a", RunConfig: map[string]json.RawMessage{"memory_namespace": json.RawMessage(`"org:a"`)}}
	renameOrg(&d, "a", "b")
	if d.Name != "b" || string(d.RunConfig["memory_namespace"]) != `"org:b"` {
		t.Fatalf("renamed = %+v", d)
	}
	d = orgdesign.Doc{Name: "a", RunConfig: map[string]json.RawMessage{"memory_namespace": json.RawMessage(`"custom"`)}}
	renameOrg(&d, "a", "b")
	if string(d.RunConfig["memory_namespace"]) != `"custom"` {
		t.Fatal("a namespace of its own was renamed")
	}
}

// An API package id names its install folder: nothing else reaches the file system.
func TestAPIPackageIDsAndFiles(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"../x", "a/b", "A", "", ".", "x/../../y"} {
		if _, err := apiPackagePath(id); err == nil {
			t.Errorf("apiPackagePath(%q) accepted", id)
		}
	}
	if _, err := apiPackagePath("acme_2"); err != nil {
		t.Errorf("a valid id refused: %v", err)
	}
	if _, _, err := readAPIPackage(filepath.Join(t.TempDir(), "missing.json")); clicore.ExitCodeFor(err) != 2 {
		t.Errorf("missing file: %v", err)
	}
}

func TestPrintInstall(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printInstall(&b, &InstallResult{Kind: "api", Item: library.Item{Name: "Acme", Version: "1.0.0"}, SHA256: "0123456789abcdef",
		LocalID: "acme", Installed: true, Result: &APIInstallResult{NodeType: "app.acme"},
		Warnings: []string{"w"}, MissingAutomations: []string{"x", "y"}})
	want := "Installed api \"Acme\" 1.0.0 from monoes.me as acme (sha256 0123456789ab verified)\n" +
		"Node type app.acme is now available in workflows (monoagentcli ref node app.acme).\n" +
		"Warning: w\nIt needs automations that are not installed: x, y\nInstall them with: monoagentcli library install automation <id>\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

// Logged out, every read asks for a login and sends nothing; status says so.
func TestReadsNeedALogin(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"list"}, {"show", "automation/x"}, {"install", "automation", "x"}, {"update"}} {
		if _, err := f.run(true, args...); err != errLogin {
			t.Errorf("%v: %v", args, err)
		}
	}
	var st Status
	f.must(&st, "status", "--offline")
	if st.LoggedIn || st.BaseURL != f.fake.URL {
		t.Fatalf("status = %+v", st)
	}
	if out := f.must(nil, "status"); out != fmt.Sprintf("Not logged in to %s. Run `monoagentcli library login` to browse and install from the library.\n", f.fake.URL) {
		t.Fatalf("text status = %q", out)
	}
}

// Log in, install an automation from the library, see it installed and up to date,
// then log out.
func TestLoginInstallUpdateLogout(t *testing.T) {
	f := newFixture(t)
	f.fake.Add("monoes", "automation", "hackernews", "Hacker News", "official", "1.1.0", pack(t, "hackernews"),
		map[string]any{"automation_id": "hackernews"})
	var st Status
	f.must(&st, "login")
	if f.signed != 1 || !st.LoggedIn || st.Method != "session" || st.User == nil || st.User.Username != "ada" {
		t.Fatalf("login = %+v", st)
	}
	var list struct{ Items []Item }
	f.must(&list, "list", "--kind", "automations")
	if len(list.Items) != 1 || list.Items[0].Installed != nil {
		t.Fatalf("list = %+v", list)
	}
	var res InstallResult
	f.must(&res, "install", "automation", "hackernews", "--dry-run")
	if res.Installed || !res.DryRun || res.LocalID != "hackernews" {
		t.Fatalf("dry run = %+v", res)
	}
	f.must(&res, "install", "automations/hackernews")
	if !res.Installed || res.Trust != automation.TrustBuiltin {
		t.Fatalf("install = %+v", res)
	}
	var installed struct{ Items []library.Record }
	f.must(&installed, "installed")
	if len(installed.Items) != 1 || installed.Items[0].LocalID != "hackernews" || !installed.Items[0].Official {
		t.Fatalf("installed = %+v", installed)
	}
	var up struct{ Updates []Update }
	f.must(&up, "update", "--dry-run")
	if len(up.Updates) != 1 || up.Updates[0].Status != "up_to_date" {
		t.Fatalf("update = %+v", up)
	}
	if _, err := f.run(true, "update", "nothing-like-it"); clicore.ExitCodeFor(err) != 2 {
		t.Fatalf("update of nothing: %v", err)
	}
	if _, err := f.run(true, "install", "org", "automations/hackernews"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("a kind mismatch: %v", err)
	}
	f.must(nil, "logout")
	f.must(&st, "status")
	if st.LoggedIn {
		t.Fatalf("after logout: %+v", st)
	}
}
