package apiconfig

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// hermetic keeps BuildStatus off the machine: no address from the environment, no Jev key.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("MONOAGENT_HTTPAPI_ADDR", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	testhome.SetHome(t, t.TempDir())
}

type probes struct {
	asked    []string
	answers  map[string][2]bool // base -> reachable, v1 answers
	wantedV1 map[string]bool
}

func (p *probes) probe(base string, wantV1 bool) (bool, bool) {
	p.asked = append(p.asked, base)
	if p.wantedV1 == nil {
		p.wantedV1 = map[string]bool{}
	}
	p.wantedV1[base] = wantV1
	a := p.answers[base]
	return a[0], a[1] && wantV1
}

func liveHeartbeat(hb daemonhb.Heartbeat) func() (daemonhb.Heartbeat, bool) {
	return func() (daemonhb.Heartbeat, bool) { return hb, true }
}

func noDaemon() (daemonhb.Heartbeat, bool) { return daemonhb.Heartbeat{}, false }

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// BuildStatus asks the heartbeat and the probe it is given and nothing else of the machine:
// what the MCP tool and the tests that stand for it rely on.
func TestBuildStatusUsesTheHeartbeatAndTheProbeItIsGiven(t *testing.T) {
	hermetic(t)
	db := openDB(t)
	p := &probes{answers: map[string][2]bool{"http://127.0.0.1:9322": {true, true}, "https://0.0.0.0:9443": {true, true}}}
	st, err := BuildStatus(context.Background(), db, Env{
		Getenv: envMap(nil), Probe: p.probe,
		Heartbeat: liveHeartbeat(daemonhb.Heartbeat{
			PID: 1, APIAddr: "127.0.0.1:9322", V1Addr: "0.0.0.0:9443", APIConfinement: "any", V1Confinement: "chat-only",
			ContextConfinement: "sandboxed", AutoConfinement: "sandboxed",
		}),
	}, "work")
	if err != nil {
		t.Fatal(err)
	}
	if st.V != 1 || st.Profile != "work" || st.Keys.Active != 0 || !st.Daemon.Running || st.Daemon.V1Addr != "0.0.0.0:9443" {
		t.Errorf("status: %+v", st)
	}
	if len(st.Listeners) != 2 {
		t.Fatalf("listeners: %+v", st.Listeners)
	}
	main, v1 := st.Listeners[0], st.Listeners[1]
	if main.Name != "main" || main.Scheme != "http" || !main.V1Answers || main.Confinement != "any" || main.ConfinementSource != "daemon" ||
		main.ContextConfinement != "sandboxed" || main.AutoConfinement != "sandboxed" {
		t.Errorf("main: %+v", main)
	}
	if v1.Name != "v1" || v1.Scheme != "https" || !v1.V1Answers || v1.Confinement != "chat-only" || v1.ContextConfinement != "chat-only" || v1.AutoConfinement != "chat-only" {
		t.Errorf("v1: %+v", v1)
	}
	// The main listener is always plain HTTP; a network one is asked over TLS first.
	if want := []string{"http://127.0.0.1:9322", "https://0.0.0.0:9443"}; !slices.Equal(p.asked, want) {
		t.Errorf("probed %v, want %v", p.asked, want)
	}
	if !p.wantedV1["http://127.0.0.1:9322"] || !p.wantedV1["https://0.0.0.0:9443"] {
		t.Errorf("both listeners are meant to serve /v1: %v", p.wantedV1)
	}
}

// Without a daemon the settings are the process's: its environment, and the saved settings
// under it.
func TestBuildStatusWithoutADaemonReadsTheEnvironmentAndTheSavedSettings(t *testing.T) {
	hermetic(t)
	db := openDB(t)
	putRow(t, db, `{"v":1,"v1_addr":"127.0.0.1:9443","confinement":"sandboxed","context_confinement":"any"}`)
	p := &probes{}
	st, err := BuildStatus(context.Background(), db, Env{Getenv: envMap(map[string]string{"MONOAGENT_API_AUTO_CONFINEMENT": "sandboxed"}), Probe: p.probe, Heartbeat: noDaemon}, "default")
	if err != nil {
		t.Fatal(err)
	}
	if st.Daemon.Running || len(st.Listeners) != 2 {
		t.Fatalf("status: %+v", st)
	}
	for _, l := range st.Listeners {
		if l.Confinement != "sandboxed" || l.ConfinementSource != "environment" || l.ContextConfinement != "sandboxed" || l.AutoConfinement != "sandboxed" {
			t.Errorf("%s: %+v", l.Name, l)
		}
	}
	if st.Listeners[1].Addr != "127.0.0.1:9443" || !st.Listeners[1].Loopback {
		t.Errorf("v1: %+v", st.Listeners[1])
	}
	// Loopback listeners are asked in the clear first, then over TLS: nothing answers here.
	if want := []string{"http://127.0.0.1:9322", "http://127.0.0.1:9443", "https://127.0.0.1:9443"}; !slices.Equal(p.asked, want) {
		t.Errorf("probed %v, want %v", p.asked, want)
	}
}

// A value of the process's environment that fails its rule is the caller's input, with the
// words the flag or the variable always had.
func TestBuildStatusReportsABadEnvironmentValueAsAnInputError(t *testing.T) {
	hermetic(t)
	for env, want := range map[string]string{
		"MONOAGENT_API_CONTEXT_CONFINEMENT": `--context-confinement (MONOAGENT_API_CONTEXT_CONFINEMENT): unknown confinement "x": use chat-only, sandboxed or any`,
		"MONOAGENT_API_AUTO_CONFINEMENT":    `--auto-confinement (MONOAGENT_API_AUTO_CONFINEMENT): unknown confinement "x": use chat-only, sandboxed or any`,
		"MONOAGENT_API_CONFINEMENT":         `unknown confinement "x": use chat-only, sandboxed or any`,
	} {
		_, err := BuildStatus(context.Background(), openDB(t), Env{Getenv: envMap(map[string]string{env: "x"}), Heartbeat: noDaemon, Probe: (&probes{}).probe}, "default")
		var in *InputError
		if !errors.As(err, &in) || err.Error() != want {
			t.Errorf("%s: %v (input error %v), want %q", env, err, errors.As(err, &in), want)
		}
	}
}

func TestBuildStatusRefusesInvalidSavedSettingsAndDamagedDocuments(t *testing.T) {
	hermetic(t)
	db := openDB(t)
	env := Env{Getenv: envMap(nil), Heartbeat: noDaemon, Probe: (&probes{}).probe}
	putRow(t, db, `{"v":1,"max_concurrent":0}`)
	_, err := BuildStatus(context.Background(), db, env, "default")
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "max_concurrent must be") {
		t.Errorf("invalid saved settings: %v", err)
	}
	putRow(t, db, `{"v":2}`)
	if _, err := BuildStatus(context.Background(), db, env, "default"); !errors.Is(err, ErrTooNew) {
		t.Errorf("a newer format: %v", err)
	}
}

// The document is a contract: every field and its place. The CLI prints it, the MCP tool and
// the desktop app read it.
func TestTheStatusDocumentKeepsItsFieldsAndTheirOrder(t *testing.T) {
	var st StatusReport
	st.V, st.Profile = 1, "p"
	st.Keys.Active = 2
	st.Daemon.Running, st.Daemon.APIAddr, st.Daemon.V1Addr = true, "127.0.0.1:9322", "0.0.0.0:9443"
	st.Auto.Available, st.Auto.KeySource = true, "vault"
	st.Listeners = []ListenerReport{
		{Name: "main", Addr: "127.0.0.1:9322", Loopback: true, V1: true, Confinement: "any", DaemonSaysNoV1: true,
			ContextConfinement: "chat-only", AutoConfinement: "chat-only", ConfinementSource: "environment", Scheme: "http", Reachable: true, V1Answers: true},
		{Name: "v1", Addr: "0.0.0.0:9443", V1: true, Confinement: "chat-only", ContextConfinement: "chat-only", AutoConfinement: "chat-only", ConfinementSource: "daemon"},
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"v":1,"profile":"p","keys":{"active":2},"daemon":{"running":true,"api_addr":"127.0.0.1:9322","v1_addr":"0.0.0.0:9443"},` +
		`"auto":{"available":true,"key_source":"vault"},"listeners":[` +
		`{"name":"main","addr":"127.0.0.1:9322","loopback":true,"v1":true,"confinement":"any","context_confinement":"chat-only","auto_confinement":"chat-only","confinement_source":"environment","scheme":"http","reachable":true,"v1_answers":true},` +
		`{"name":"v1","addr":"0.0.0.0:9443","loopback":false,"v1":true,"confinement":"chat-only","context_confinement":"chat-only","auto_confinement":"chat-only","confinement_source":"daemon","reachable":false,"v1_answers":false}]}`
	if string(b) != want {
		t.Errorf("the document changed:\n got %s\nwant %s", b, want)
	}
}

// A running daemon is believed about what it serves: what the environment names next to it is
// listed only if something answers there, and a listener the daemon says it did not mount /v1
// on is not asked for it.
func TestBuildStatusIsNotMisledByTheEnvironmentWhenADaemonRuns(t *testing.T) {
	hermetic(t)
	p := &probes{answers: map[string][2]bool{"http://127.0.0.1:9322": {true, true}}}
	st, err := BuildStatus(context.Background(), openDB(t), Env{
		Getenv: envMap(map[string]string{"MONOAGENT_API_V1_ADDR": "127.0.0.1:7"}), Probe: p.probe,
		// The daemon serves the HTTP API and reports no policy for it, but a context maximum:
		// it knows /v1 and did not mount it there.
		Heartbeat: liveHeartbeat(daemonhb.Heartbeat{PID: 1, APIAddr: "127.0.0.1:9322", ContextConfinement: "chat-only"}),
	}, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Listeners) != 1 {
		t.Fatalf("the listener the environment names does not answer, so it is not listed next to a running daemon: %+v", st.Listeners)
	}
	main := st.Listeners[0]
	if !main.DaemonSaysNoV1 || main.V1 || main.ConfinementSource != "environment" || main.Confinement != "any" {
		t.Errorf("main: %+v", main)
	}
	if p.wantedV1["http://127.0.0.1:9322"] {
		t.Error("a listener the daemon did not mount /v1 on must not be asked for it")
	}
	b, _ := json.Marshal(main)
	if strings.Contains(string(b), "DaemonSaysNoV1") || strings.Contains(string(b), "daemon_says") {
		t.Errorf("that is the builder's note, not part of the document: %s", b)
	}
}

// What the auto model may pick is the daemon's when it is running, chat-only when it predates
// the setting, whatever this process's environment says.
func TestBuildStatusAutoMaximumFollowsTheDaemon(t *testing.T) {
	hermetic(t)
	db := openDB(t)
	env := func(hb daemonhb.Heartbeat) Env {
		return Env{Getenv: envMap(map[string]string{"MONOAGENT_API_AUTO_CONFINEMENT": "any"}), Probe: (&probes{}).probe, Heartbeat: liveHeartbeat(hb)}
	}
	st, err := BuildStatus(context.Background(), db, env(daemonhb.Heartbeat{PID: 1, APIAddr: "127.0.0.1:9322", APIConfinement: "any", ContextConfinement: "chat-only"}), "default")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Listeners[0].AutoConfinement; got != "chat-only" {
		t.Errorf("a daemon that predates the setting: auto may pick up to %q, want chat-only", got)
	}
	st, err = BuildStatus(context.Background(), db, env(daemonhb.Heartbeat{PID: 1, APIAddr: "127.0.0.1:9322", APIConfinement: "any", ContextConfinement: "chat-only", AutoConfinement: "sandboxed"}), "default")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Listeners[0].AutoConfinement; got != "sandboxed" {
		t.Errorf("the daemon's own maximum: %q, want sandboxed", got)
	}
}

func TestBuildStatusSaysWhetherTheAutoModelWorks(t *testing.T) {
	hermetic(t)
	st, err := BuildStatus(context.Background(), openDB(t), Env{Getenv: envMap(nil), Probe: (&probes{}).probe, Heartbeat: noDaemon}, "default")
	if err != nil {
		t.Fatal(err)
	}
	if st.Auto.Available || st.Auto.Missing == "" {
		t.Errorf("with no Jev key the auto model is off and says what it needs: %+v", st.Auto)
	}
}

// Whether /v1 is asked is the caller's call: a listener not expected to serve it is not
// asked, whatever it would answer.
func TestProbeListenerAsksForV1OnlyWhenWanted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/models":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if reachable, v1 := ProbeListener(srv.URL, false); !reachable || v1 {
		t.Errorf("not wanted: reachable=%v v1=%v", reachable, v1)
	}
	if reachable, v1 := ProbeListener(srv.URL, true); !reachable || !v1 {
		t.Errorf("wanted: reachable=%v v1=%v", reachable, v1)
	}
}

// What answers is whatever is at the address: a redirect is not followed anywhere else.
func TestProbeListenerDoesNotFollowARedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	if reachable, v1 := ProbeListener(srv.URL, true); reachable || v1 {
		t.Errorf("a redirect answered as the API: reachable=%v v1=%v", reachable, v1)
	}
}
