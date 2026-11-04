package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// openBindings are the bound methods that run signed out: the sign-in screen
// and what it offers. Every other method of App must refuse; a new binding is
// gated unless it is added here with its reason.
var openBindings = map[string]string{
	"AccountStatus":           "the sign-in screen asks it what to show (runs `account status`, open in the CLI)",
	"AccountLogin":            "the browser sign-in (`account login`)",
	"AccountLoginCancel":      "cancels that sign-in",
	"AccountLoginEmailSend":   "the email-code sign-in: sends the code",
	"AccountLoginEmailVerify": "the email-code sign-in: trades the code for a session",
	"AccountLogout":           "ends the session (`account logout` is open in the CLI)",
	"AppSelfUpdate":           "the sign-in screen offers the update (`update` is open in the CLI, so a broken install can be fixed)",
	"QuitApp":                 "the notice of an update that is shown on the sign-in screen, too, ends with Restart now; it only quits the app",
	"CheckForUpdate":          "finds the release the sign-in screen offers (`update --check`)",
	"DoomsdayStop":            "the operator stops a run whatever the sign-in says (user decision); the operator gate still applies and no other doomsday binding is open (TestOnlyDoomsdayStopRunsSignedOut)",
	"OpenURL":                 "the sign-in fallback link; signed out it opens only the monoes.me host (TestOpenURLSignedOutOpensOnlyTheSignInHost)",
}

// callZero calls method m of a with zero arguments, turning a panic into a
// failure of the test.
func callZero(t *testing.T, a *App, m reflect.Method) (out []reflect.Value) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked signed out: %v", m.Name, r)
		}
	}()
	args := []reflect.Value{reflect.ValueOf(a)}
	for i := 1; i < m.Type.NumIn(); i++ {
		args = append(args, reflect.Zero(m.Type.In(i)))
	}
	return m.Func.Call(args)
}

// Signed out, every bound method but the open ones refuses before it does
// anything: the gate counts exactly one refusal, an error result is the
// login-required error and a JSON string result says login_required.
func TestEveryBindingRefusesSignedOut(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	a := newTestApp(t)
	typ := reflect.TypeOf(a)
	if typ.NumMethod() < 300 {
		t.Fatalf("App has %d methods; the walk is not seeing the bindings", typ.NumMethod())
	}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if _, open := openBindings[m.Name]; open {
			continue
		}
		before := gateRefusals.Load()
		out := callZero(t, a, m)
		if got := gateRefusals.Load() - before; got != 1 {
			t.Errorf("%s ran signed out: the gate refused %d times, want 1 (begin it with a.signedOut(), or add it to openBindings with a reason)", m.Name, got)
			continue
		}
		if n := len(out); n > 0 {
			last := out[n-1]
			switch {
			case m.Type.Out(n-1) == errType:
				if err, _ := last.Interface().(error); !account.IsLoginRequired(err) {
					t.Errorf("%s signed out returned error %v, want the login-required error", m.Name, err)
				}
			case n == 1 && last.Kind() == reflect.String:
				var doc struct {
					Error         string `json:"error"`
					LoginRequired bool   `json:"login_required"`
				}
				if json.Unmarshal([]byte(last.String()), &doc) != nil || !doc.LoginRequired || !strings.HasPrefix(doc.Error, account.LoginRequiredMessage) {
					t.Errorf("%s signed out returned %q, want the login_required document", m.Name, last.String())
				}
			}
		}
	}
	for name := range openBindings {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("openBindings lists %s, which App no longer has", name)
		}
	}
}

// Signed in, the gate lets the bindings through.
func TestSignedOutIsNilSignedIn(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	a := newTestApp(t)
	before := gateRefusals.Load()
	if err := a.signedOut(); err != nil {
		t.Fatalf("signedOut signed in: %v", err)
	}
	a.ready.Store(true)
	if !a.IsReady() {
		t.Fatal("IsReady signed in does not answer the ready flag")
	}
	if gateRefusals.Load() != before {
		t.Fatal("the gate counted a refusal signed in")
	}
}

// A guard that does not allow work makes Require refuse without one installed too.
func TestSignedOutFailsClosedWithNoGuard(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	account.Install(nil)
	if err := (&App{}).signedOut(); !account.IsLoginRequired(err) {
		t.Fatalf("signedOut with no guard = %v, want the login-required error", err)
	}
}

func TestLoginRequiredJSONIsTheCLIDocument(t *testing.T) {
	err := &account.LoginRequiredError{Status: account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn}}
	var doc map[string]any
	if json.Unmarshal([]byte(loginRequiredJSON(err)), &doc) != nil {
		t.Fatal("not JSON")
	}
	if doc["login_required"] != true || doc["code"] != "auth_or_connection" || !strings.HasPrefix(doc["error"].(string), account.LoginRequiredMessage) {
		t.Fatalf("document = %v", doc)
	}
	if acc, _ := doc["account"].(map[string]any); acc["reason"] != "not_logged_in" {
		t.Fatalf("account = %v", doc["account"])
	}
}

func TestOpenURLSignedOutOpensOnlyTheSignInHost(t *testing.T) {
	host := account.Host()
	for raw, want := range map[string]bool{
		host + "/oauth/authorize?x=1": true,
		host:                          true,
		"https://example.com/":        false,
		"file:///etc/passwd":          false,
		"javascript:alert(1)":         false,
		"":                            false,
	} {
		if got := signInURL(raw); got != want {
			t.Errorf("signInURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

// The vault images are not served signed out.
func TestVaultImageHandlerRefusesSignedOut(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	a := newTestApp(t)
	rec := httptest.NewRecorder()
	vaultImageHandler(a).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vault-image/x.png", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("signed out: status %d, want 403", rec.Code)
	}
}

// The signed-in work follows the session: nothing starts signed out, it
// starts when the session allows work and stops when it no longer does.
func TestSyncSignedInFollowsTheSession(t *testing.T) {
	g, fx := accounttest.InstallWithFixture(t, accounttest.SignedIn)
	locked := account.NewGuard(account.GuardOptions{Store: account.OpenStore(t.TempDir(), account.NewMemorySealer()), Now: fx.Clock.Now})
	t.Cleanup(locked.Close)
	a := newTestApp(t)
	var starts, stops int
	a.work.start = func() { starts++ }
	a.work.stop = func() { stops++ }

	account.Install(locked)
	if a.syncSignedIn() || starts != 0 {
		t.Fatal("signed out, the signed-in work started")
	}
	account.Install(g)
	if !a.syncSignedIn() || starts != 1 {
		t.Fatal("signed in, the signed-in work did not start")
	}
	if a.syncSignedIn() || starts != 1 {
		t.Fatal("a second sync with no change started it again")
	}
	account.Install(locked)
	if !a.syncSignedIn() || stops != 1 {
		t.Fatal("after sign-out, the signed-in work was not stopped")
	}
}

// Signed out, startWatchers starts no watcher, and stopSignedIn takes the
// ready flag down and stops them all.
func TestWatchersDoNotStartSignedOut(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	a := newTestApp(t)
	a.startWatchers()
	if a.orgWatcher != nil || a.docWatcher != nil || a.capWatcher != nil || a.imgWatcher != nil || a.taskWatchStop != nil {
		t.Fatal("a watcher started signed out")
	}
	stopped := false
	a.taskWatchStop = func() { stopped = true }
	a.ready.Store(true)
	a.stopSignedIn()
	if !stopped || a.taskWatchStop != nil || a.ready.Load() {
		t.Fatal("stopSignedIn left the task watcher or the ready flag up")
	}
}

// watchSession tells the page when the session changed.
func TestWatchSessionStopsWithItsContext(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	defer func(d time.Duration) { sessionWatchInterval = d }(sessionWatchInterval)
	sessionWatchInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { (&App{}).watchSession(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(errors.New("watchSession did not stop"))
	}
}

// startup starts the signed-in work only through syncSignedIn: a source
// check, since startup needs the Wails runtime.
func TestStartupStartsNothingSignedOut(t *testing.T) {
	b, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	body := src[strings.Index(src, "func (a *App) startup("):]
	body = body[:strings.Index(body, "\n}\n")]
	for _, call := range []string{"restartOrgWatcher", "restartDocumentWatcher", "restartImageWatcher", "restartTaskWatcher", "initChatSupervisor", "prepareDatabase", "BootAutomations", "QueryRow"} {
		if strings.Contains(body, call) {
			t.Errorf("startup calls %s directly; it belongs in startSignedIn", call)
		}
	}
	if !strings.Contains(body, "a.syncSignedIn()") || !strings.Contains(body, "go a.watchSession(ctx)") {
		t.Error("startup no longer hands the signed-in work to syncSignedIn and watchSession")
	}
}

// A sign-in the CLI reports before the guard's next poll makes the guard
// re-read the session at once, and the signed-in work starts.
func TestFollowSessionRereadsTheSessionTheCLIReports(t *testing.T) {
	account.InstallForTest(t, nil) // restores the process guard when the test ends
	t.Setenv("HOME", t.TempDir())
	fx := accounttest.New(t)
	fx.Clock.Set(time.Now())
	a := newTestApp(t)
	started := make(chan struct{}, 1)
	a.work.start = func() { started <- struct{}{} }

	a.followSession(`{"v":1,"state":"ok"}`) // before startup: nothing to follow
	a.startAccountGuard()
	t.Cleanup(a.stopAccountGuard)
	if account.CurrentStatus().Allowed() {
		t.Fatal("allowed with no session")
	}
	sess := &account.Session{V: 1, Host: account.HostURL, User: &account.User{ID: "u1"}, AccessToken: fx.Token(accounttest.TokenOptions{Sub: "u1"})}
	if err := account.OpenStore("", account.NewMemorySealer()).Save(sess); err != nil {
		t.Fatal(err)
	}
	a.followSession(`{"v":1,"state":"ok"}`)
	if !account.CurrentStatus().Allowed() {
		t.Fatal("the guard did not re-read the session the CLI reported")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the signed-in work did not start")
	}
}
