//go:build devaccount && !windows

package accountsmoke

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/testhome"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

// Tests run in a throwaway HOME; the CLI they build lives in it too, so it
// goes when testhome removes the home.
func TestMain(m *testing.M) { testhome.Main(m) }

// cli builds the CLI with the dev tag, once per test run.
func cli(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		if binDir, buildErr = os.MkdirTemp(os.Getenv("HOME"), "accountsmoke-"); buildErr != nil {
			return
		}
		root, _ := filepath.Abs("../..")
		cmd := exec.Command("go", "build", "-tags", "devaccount", "-ldflags", "-X main.version=v0.0.0-smoke",
			"-o", filepath.Join(binDir, "monoagentcli"), "./cmd/monoagentcli")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build -tags devaccount: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return filepath.Join(binDir, "monoagentcli")
}

// fakeMonomind stands in for the monomind CLI: the handshake, one canned agent turn, an org
// validation and an org daemon that runs until stopped. Every call is logged.
const fakeMonomind = `#!/bin/sh
echo "monomind $*" >> "$FAKE_MONOMIND_LOG"
case "$1" in
  --version) echo '{"v":1,"version":"2.16.5","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1","org-tool-providers"]}' ;;
  agent) [ "$2" = "exec" ] || exit 2
    echo '{"v":1,"type":"start","runtime":"claude","pid":1}'
    echo '{"v":1,"type":"assistant","text":"smoke-ok"}'
    echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"smoke-ok"}'
    echo '{"v":1,"type":"done","exit_code":0}' ;;
  org) case "$2" in
    serve) trap 'exit 0' TERM INT; while :; do sleep 1; done ;;
    validate) echo '{"valid":true}' ;;
    *) echo '{}' ;;
  esac ;;
  *) exit 2 ;;
esac
`

// The edge is a switch in front of the fake monoes.me's own handler, so that a test can make
// monoes.me unreachable or have it answer a refresh grant in a way the fake never would. The binary
// talks to one server, the fake's, and the rig depends on nothing of it but the OAuth wire shapes.
const (
	modePass          = iota // everything reaches the fake
	modeDown                 // the connection is dropped: monoes.me is unreachable
	modeInvalidGrant         // a refresh-token grant is answered invalid_grant: monoes.me says no
	modeServerError          // ... answered 500
	modeInvalidClient        // ... answered invalid_client
	modeInvalidTarget        // ... answered invalid_target
	modeGarbage              // ... answered 200 with a page that is not JSON
	modeUnknownKey           // ... answered 200 with a token signed by a key no build pins
	modeHold                 // ... reaches the fake at once, which rotates the token, and is answered holdAnswer later
)

// holdAnswer is how long modeHold keeps back the answer to a refresh grant that the fake has already
// acted on: time for a test to interrupt the caller while the refresh token is rotated and the answer
// is still on its way.
const holdAnswer = 6 * time.Second

// What a refresh-token grant is answered with, per mode. Only invalid_grant is monoes.me refusing
// the account (D27); every other answer is trouble on the way, and keeps the grace.
var refreshAnswer = map[int32]struct {
	status int
	body   string
}{
	modeInvalidGrant:  {http.StatusBadRequest, `{"error":"invalid_grant","error_description":"the account was blocked"}`},
	modeServerError:   {http.StatusInternalServerError, `{"error":"server_error"}`},
	modeInvalidClient: {http.StatusBadRequest, `{"error":"invalid_client"}`},
	modeInvalidTarget: {http.StatusBadRequest, `{"error":"invalid_target"}`},
	modeGarbage:       {http.StatusOK, `<html>down for maintenance</html>`},
}

// reuseWindow is how long monoes.me answers a refresh token that is presented again with the answer it
// gave the first time (plan A, Task 3: refreshTokenReuseInterval). A repeat after the window is a replay:
// monoes.me deletes every refresh token of the account. The fake monoes.me keeps no window and punishes
// every repeat, so the edge keeps it, on a clock of its own that a scenario can move (later).
const reuseWindow = 300 * time.Second

type edge struct {
	mode     atomic.Int32
	grants   atomic.Int32  // refresh-token grants that reached the edge, whatever it did with them
	drops    atomic.Int32  // requests of any kind that reached the edge while it was down, and were dropped
	skew     atomic.Int64  // how far monoes.me's clock is ahead of the test's, in nanoseconds
	unpinned func() string // a token signed by a key no build pins, for modeUnknownKey

	mu   sync.Mutex
	seen map[string]firstAnswer // the refresh tokens that monoes.me has rotated, by token
}

// firstAnswer is what monoes.me remembers of a refresh token it has rotated: when it first saw it and
// the answer it gave.
type firstAnswer struct {
	at  time.Time
	rec *httptest.ResponseRecorder
}

// later moves monoes.me's clock d ahead of the test's, so that a scenario can let the reuse window pass
// without waiting for it.
func (e *edge) later(d time.Duration) { e.skew.Add(int64(d)) }

func (e *edge) now() time.Time { return time.Now().Add(time.Duration(e.skew.Load())) }

// repeat is the first answer to a refresh token that is presented again inside the reuse window.
func (e *edge) repeat(token string) (*httptest.ResponseRecorder, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.seen[token]
	return a.rec, ok && e.now().Sub(a.at) < reuseWindow
}

// remember keeps the answer given to a refresh token the first time monoes.me rotates it.
func (e *edge) remember(token string, rec *httptest.ResponseRecorder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.seen[token]; !ok && rec.Code == http.StatusOK {
		e.seen[token] = firstAnswer{e.now(), rec}
	}
}

// reply writes a recorded answer.
func reply(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// newEdge wraps the fake's handler. It is installed before the first request, so nothing races it in
// practice; httptest documents changes to Config as valid only before Start and the swap is not
// synchronized with the server's goroutines, so this package is not run with -race (CI does not).
func newEdge(fake *libraryfake.Server) *edge {
	e := &edge{seen: map[string]firstAnswer{}}
	inner := fake.Config.Handler
	fake.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refresh, token := false, ""
		if r.Method == http.MethodPost && r.URL.Path == "/api/auth/oauth2/token" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body)) // the fake still reads what it was sent
			form, _ := url.ParseQuery(string(body))
			if refresh = form.Get("grant_type") == "refresh_token"; refresh {
				token = form.Get("refresh_token")
			}
		}
		mode := e.mode.Load()
		held := refresh && mode == modeHold
		if refresh && !held {
			e.grants.Add(1) // a held grant is counted once monoes.me has acted on it, below
		}
		if mode == modeDown {
			e.drops.Add(1)
			if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
				c.Close()
			}
			return
		}
		if refresh && mode == modePass {
			if rec, ok := e.repeat(token); ok { // inside the window monoes.me repeats its first answer
				reply(w, rec)
				return
			}
		}
		if refresh && (mode == modePass || held) {
			rec := httptest.NewRecorder()
			inner.ServeHTTP(rec, r) // monoes.me rotates the refresh token now...
			e.remember(token, rec)
			if held {
				e.grants.Add(1)
				select {
				case <-time.After(holdAnswer): // ...and the caller hears of it later
				case <-r.Context().Done(): // unless the caller is gone: nothing is left to answer
					return
				}
			}
			reply(w, rec)
			return
		}
		if mode == modeUnknownKey && refresh {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": e.unpinned(), "refresh_token": "rt-stranger",
				"token_type": "Bearer", "expires_in": 3600, "scope": "openid"})
			return
		}
		if a, ok := refreshAnswer[mode]; ok && refresh {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(a.status)
			_, _ = w.Write([]byte(a.body))
			return
		}
		inner.ServeHTTP(w, r)
	})
	return e
}

func (e *edge) set(mode int32) { e.mode.Store(mode) }

// refreshes is how many refresh-token grants have been presented to monoes.me so far.
func (e *edge) refreshes() int { return int(e.grants.Load()) }

// dropped is how many requests reached monoes.me while it was down. A refresh that cannot find monoes.me
// stops at the endpoint discovery and never sends its grant, so it shows here and not in refreshes.
func (e *edge) dropped() int { return int(e.drops.Load()) }

type rigOptions struct {
	ttl     time.Duration // how long the fake's access tokens live (default: its own, an hour)
	enforce time.Time     // the date the binary enforces from, in its environment; zero leaves the variable unset
	sharing *rig          // another install of the same account: this rig uses that rig's monoes.me, in a HOME of its own
}

// A rig is one throwaway HOME, one fake monoes.me with its edge, one fake monomind and the CLI
// built with the dev tag. Everything the CLI writes lands under home; everything else the test
// needs lives in dir, so that "nothing was written" can be asserted of home.
type rig struct {
	t       *testing.T
	bin     string
	dir     string
	home    string
	fake    *libraryfake.Server
	edge    *edge
	enforce time.Time
	ports   map[string]int
	files   *int
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	r := &rig{t: t, bin: cli(t), dir: t.TempDir(), enforce: o.enforce, ports: map[string]int{}, files: new(int)}
	r.home = filepath.Join(r.dir, "home")
	must(t, os.Mkdir(r.home, 0o700))
	if o.sharing != nil {
		r.fake, r.edge = o.sharing.fake, o.sharing.edge
	} else {
		r.fake = libraryfake.New()
		t.Cleanup(r.fake.Close)
		if o.ttl > 0 {
			r.fake.AccessTTL = o.ttl
		}
		r.edge = newEdge(r.fake)
		stranger := accounttest.New(t) // a throwaway signing key that the binary under test does not pin
		r.edge.unpinned = func() string { return stranger.Token(accounttest.TokenOptions{Sub: "u-ada"}) }
	}
	must(t, os.WriteFile(filepath.Join(r.dir, "passphrase"), []byte("smoke-passphrase\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(r.dir, "monomind"), []byte(fakeMonomind), 0o755))
	return r
}

// sub is the rig as a subtest sees it: a failure inside must fail the subtest, not its parent.
func (r *rig) sub(t *testing.T) *rig {
	c := *r
	c.t = t
	return &c
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// port is a free loopback port, the same one for every call with the same name.
func (r *rig) port(name string) int {
	if _, ok := r.ports[name]; !ok {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		must(r.t, err)
		defer l.Close()
		r.ports[name] = l.Addr().(*net.TCPAddr).Port
	}
	return r.ports[name]
}

func (r *rig) addr(name string) string { return "127.0.0.1:" + strconv.Itoa(r.port(name)) }

// env is the whole environment of a process: nothing of the developer's own leaks in.
func (r *rig) env(extra ...string) []string {
	if !r.enforce.IsZero() {
		extra = append([]string{"MONOAGENT_DEV_ENFORCE_FROM=" + r.enforce.UTC().Format(time.RFC3339)}, extra...)
	}
	return append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + r.home,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(r.dir, "no-bus"), // no Secret Service: the vault uses the file keyring, as on a headless host
		"MONOES_BASE_URL=" + r.fake.URL,
		"MONOAGENT_ALLOW_FILE_KEYRING=1", "MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE=" + filepath.Join(r.dir, "passphrase"),
		"MONOMIND_BIN=" + filepath.Join(r.dir, "monomind"), "FAKE_MONOMIND_LOG=" + filepath.Join(r.dir, "monomind.log"),
		"MONOAGENT_SUMMARY_RUNTIME=off",
		"MONOAGENT_WEBHOOK_ADDR=" + r.addr("webhook"), "MONOAGENT_EXTENSION_PORT=" + strconv.Itoa(r.port("bridge")),
		"MONOAGENT_API_ADDR=" + r.addr("api"),
		// The binary looks for agent markers no higher than this test process:
		// an agent shell that runs the smoke must not mark it (devaccount only).
		operator.AncestorStopEnv + "=" + strconv.Itoa(os.Getpid()),
	}, extra...)
}

type result struct {
	code           int
	stdout, stderr string
}

// onProfile is args acting on the Default profile, which every machine has. The smoke tests are about
// the account gate; the profile gate refuses a scoped command that does not name its profile, and
// a flag after the subcommand is read as well as one before it.
func onProfile(args []string) []string {
	for i, a := range args {
		switch a {
		case "--profile":
			return args
		case "--": // the words after it belong to the command: the flag goes before them
			return append(append(append([]string{}, args[:i]...), "--profile", "default"), args[i:]...)
		}
	}
	return append(append([]string{}, args...), "--profile", "default")
}

func (r *rig) run(args ...string) result {
	r.t.Helper()
	return r.runAsGiven(onProfile(args)...)
}

// runAsGiven runs args exactly as written, with no profile added.
func (r *rig) runAsGiven(args ...string) result {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env, cmd.Dir = r.env(), r.dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	var ee *exec.ExitError
	switch err := cmd.Run(); {
	case errors.As(err, &ee):
		return result{ee.ExitCode(), out.String(), errb.String()}
	case err != nil:
		r.t.Fatalf("run %v: %v", args, err)
	}
	return result{0, out.String(), errb.String()}
}

func mustExit(t testing.TB, res result, want int) {
	t.Helper()
	if res.code != want {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", res.code, want, res.stdout, res.stderr)
	}
}

func mustJSON(t testing.TB, s string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), into); err != nil {
		t.Fatalf("not JSON (%v): %s", err, s)
	}
}

// proc is a long-running process of the rig (a daemon, a server, a bridge); its output goes to
// <first argument>.log in the rig's folder.
type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (r *rig) start(args ...string) *proc { return r.startWith(nil, args...) }

// startWith is start with more environment for this one process: a later value replaces an earlier one.
func (r *rig) startWith(extra []string, args ...string) *proc {
	r.t.Helper()
	log, err := os.Create(filepath.Join(r.dir, args[0]+".log"))
	must(r.t, err)
	args = onProfile(args)
	cmd := exec.Command(r.bin, args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = r.env(extra...), r.dir, log, log
	must(r.t, cmd.Start())
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	r.t.Cleanup(p.stop)
	return p
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop asks the process to end (SIGTERM, as a service manager does) and kills it after 20 seconds.
func (p *proc) stop() {
	if !p.alive() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (r *rig) waitFor(what string, d time.Duration, ok func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(d); !ok(); time.Sleep(250 * time.Millisecond) {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out after %v waiting for %s", d, what)
		}
	}
}

// read returns a file of the rig's folder, "" when it is not there.
func (r *rig) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(r.dir, rel))
	return string(b)
}

// execution is one row of the CLI's database, as the daemon left it (Status "" when there is none).
// executionError is the error_message the run recorded ("" while it has none).
func (r *rig) executionError(id string) string {
	r.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(r.home, ".monoagent", "monoagent.db"))
	must(r.t, err)
	defer db.Close()
	var msg sql.NullString
	_ = db.QueryRow(`SELECT error_message FROM workflow_executions WHERE id = ?`, id).Scan(&msg)
	return msg.String
}

func (r *rig) execution(id string) (e struct {
	Status string
	PID    int
}) {
	r.t.Helper()
	path := filepath.Join(r.home, ".monoagent", "monoagent.db")
	if _, err := os.Stat(path); err != nil {
		return e
	}
	db, err := sql.Open("sqlite", path)
	must(r.t, err)
	defer db.Close()
	_ = db.QueryRow(`SELECT status, COALESCE(pid, 0) FROM workflow_executions WHERE id = ?`, id).Scan(&e.Status, &e.PID)
	return e
}

// accountReport is the account object that the daemon's heartbeat, GET /health and the bridge's ping
// carry: {state, reason, valid_until, enforced} (index section 3.4 items 4, 5 and 7; spec A4).
type accountReport struct {
	State      string `json:"state"`
	Reason     string `json:"reason"`
	ValidUntil string `json:"valid_until"`
	Enforced   *bool  `json:"enforced"`
}

// is checks the report against what the machine is: its state, whether the verdict is enforced (a
// warn period reports locked and not enforced), and whether a token's expiry is named, which a
// session has and a machine with no session has not.
func (a *accountReport) is(t testing.TB, state string, enforced, hasSession bool) {
	t.Helper()
	if a == nil || a.State != state || a.Enforced == nil || *a.Enforced != enforced || (a.ValidUntil != "") != hasSession {
		t.Fatalf("the account report is %+v, want state %s, enforced %v, session %v", a, state, enforced, hasSession)
	}
}

// heartbeatAccount is what the daemon last wrote: its pid and the account it reports (nil before
// a heartbeat, and while the gate is dormant).
func (r *rig) heartbeatAccount() (pid int, a *accountReport) {
	var hb struct {
		PID     int
		Account *accountReport
	}
	if b, err := os.ReadFile(filepath.Join(r.home, ".monoagent", "daemon-heartbeat.json")); err == nil && json.Unmarshal(b, &hb) == nil {
		return hb.PID, hb.Account
	}
	return 0, nil
}

// heartbeat is the pid and the account state and reason ("" before a heartbeat).
func (r *rig) heartbeat() (pid int, state, reason string) {
	pid, a := r.heartbeatAccount()
	if a == nil {
		return pid, "", ""
	}
	return pid, a.State, a.Reason
}

func (r *rig) accountState() string { _, state, _ := r.heartbeat(); return state }

func (r *rig) startDaemon() *proc {
	r.t.Helper()
	p := r.start("daemon", "--api-addr", r.addr("api"), "--bridge=false")
	r.waitFor("the daemon's heartbeat", 60*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(r.home, ".monoagent", "daemon-heartbeat.json"))
		return err == nil && p.alive()
	})
	return p
}

// waitWorkflow is a manual trigger followed by a wait node: a run that lasts about seconds.
func waitWorkflow(name string, seconds int) string {
	return fmt.Sprintf(`{"name":%q,"nodes":[
 {"id":"t","type":"trigger.manual","name":"Start","config":{}},
 {"id":"w","type":"core.wait","name":"Wait","config":{"duration":%d}}],
 "connections":[{"id":"t-w","source":"t","target":"w"}]}`, name, seconds)
}

// workflow imports def, activates it when asked, and returns its id.
func (r *rig) workflow(def string, activate bool) string {
	r.t.Helper()
	*r.files++
	path := filepath.Join(r.dir, "wf-"+strconv.Itoa(*r.files)+".json")
	must(r.t, os.WriteFile(path, []byte(def), 0o600))
	res := r.run("--json", "workflow", "import", "--file", path)
	mustExit(r.t, res, 0)
	var doc struct{ ID string }
	mustJSON(r.t, res.stdout, &doc)
	if activate {
		mustExit(r.t, r.run("workflow", "activate", doc.ID), 0)
	}
	return doc.ID
}

// enqueue starts a run for the daemon to adopt and returns its execution id.
func (r *rig) enqueue(workflowID string) string {
	r.t.Helper()
	res := r.run("--json", "workflow", "run", workflowID, "--no-wait")
	mustExit(r.t, res, 0)
	var doc struct {
		ExecutionID string `json:"execution_id"`
	}
	mustJSON(r.t, res.stdout, &doc)
	return doc.ExecutionID
}
