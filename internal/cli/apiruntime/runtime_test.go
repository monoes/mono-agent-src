package apiruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/openaiapi"
	"github.com/monoes/mono-agent/internal/testdb"
)

func newAPIRuntimeForTest(t *testing.T, f Flags) (*Runtime, error) {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	t.Setenv("MONOAGENT_API_CONFINEMENT", "")
	t.Setenv("MONOAGENT_API_CONTEXT_CONFINEMENT", "")
	t.Setenv("MONOAGENT_API_AUTO_CONFINEMENT", "")
	t.Setenv("MONOAGENT_API_V1_ADDR", "")
	t.Setenv("MONOAGENT_API_MAX_CONCURRENT", "")
	t.Setenv("MONOAGENT_API_TURN_TIMEOUT", "")
	db := testdb.Open(t)
	return newAPIRuntime(db.DB, f, func(string, ...any) {})
}

// newAPIRuntimeSharingHome is a second process over the same home: it does not
// reset HOME, so it sees the working folders the first one holds.
func newAPIRuntimeSharingHome(t *testing.T, f Flags, logf func(string, ...any)) *Runtime {
	t.Helper()
	db := testdb.Open(t)
	rt, err := newAPIRuntime(db.DB, f, logf)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// A process that serves no /v1 must not touch the working folders of one that
// does: the gateway is built only when something is going to serve it.
func TestGatewayIsBuiltOnlyWhenSomethingServesIt(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.built() != nil {
		t.Fatal("newAPIRuntime must not build the gateway")
	}
	if rt.MainMount("0.0.0.0:9322") != nil || rt.built() != nil {
		t.Error("an off-loopback main listener serves no /v1, so no gateway is needed")
	}
	if rt.MainMount("127.0.0.1:9322") == nil || rt.built() == nil {
		t.Error("serving /v1 on a loopback main listener builds the gateway")
	}
}

// Two server processes over one home would share the working folders and empty
// each other's files, so the second serves no /v1 and says why.
func TestSecondProcessDoesNotServeV1WhileAnotherOwnsTheFolders(t *testing.T) {
	first, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if first.MainMount("127.0.0.1:9322") == nil {
		t.Fatal("the first process must serve /v1")
	}

	var logs []string
	second := newAPIRuntimeSharingHome(t, Flags{v1Addr: "127.0.0.1:0"}, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if second.MainMount("127.0.0.1:9322") != nil {
		t.Error("a second process must not serve /v1 over the same folders")
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "already") {
		t.Errorf("it must say why: %q", joined)
	}
	if _, err := second.StartV1(context.Background()); !errors.Is(err, openaiapi.ErrScratchBusy) {
		t.Errorf("an explicit --v1-addr must fail when another process owns the folders: %v", err)
	}
	if got := second.confinementReport("127.0.0.1:9322", false); got != "" {
		t.Errorf("a process that serves no /v1 reports no confinement: %q", got)
	}

	first.Drain() // the first process stops; the folders are free again
	third := newAPIRuntimeSharingHome(t, Flags{}, func(string, ...any) {})
	if third.MainMount("127.0.0.1:9322") == nil {
		t.Error("a process that starts after the first has stopped serves /v1")
	}
}

func TestComposeRoutesKeepsEveryRegistrar(t *testing.T) {
	mark := func(path string) func(*http.ServeMux) {
		return func(mux *http.ServeMux) {
			mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, path) })
		}
	}
	mux := http.NewServeMux()
	composeRoutes(mark("/org-endpoint/x"), nil, mark("/v1/probe"))(mux)
	for _, path := range []string{"/org-endpoint/x", "/v1/probe"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || rec.Body.String() != path {
			t.Errorf("%s: %d %q — a registrar was lost", path, rec.Code, rec.Body)
		}
	}
}

func TestMainMountOnlyOnLoopback(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	for addr, wantMount := range map[string]bool{
		"127.0.0.1:9322": true, "localhost:9322": true, "[::1]:9322": true,
		"0.0.0.0:9322": false, ":9322": false, "10.0.0.5:9322": false,
	} {
		if got := rt.MainMount(addr) != nil; got != wantMount {
			t.Errorf("mainMount(%q) mounted = %v, want %v", addr, got, wantMount)
		}
	}

	// A mounted main listener answers /v1 (401 without a key), and nothing is
	// mounted for an off-loopback bind.
	mux := http.NewServeMux()
	rt.MainMount("127.0.0.1:9322")(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/v1/models on the main listener: %d, want 401", rec.Code)
	}
}

func TestAPIRuntimeRejectsBadSettingsAsInvalidInput(t *testing.T) {
	for name, f := range map[string]Flags{
		"unknown confinement":           {confinement: "everything"},
		"unknown context confinement":   {contextConfinement: "everything"},
		"negative max":                  {maxConcurrent: -1},
		"an explicit zero max":          {maxConcurrent: 0, maxConcurrentSet: true},
		"an absurd max":                 {maxConcurrent: 1 << 40},
		"v1 address without a port":     {v1Addr: "9443"},
		"v1 address, port not a number": {v1Addr: "0.0.0.0:abc"},
		"v1 address, port out of range": {v1Addr: "0.0.0.0:99999"},
	} {
		if _, err := newAPIRuntimeForTest(t, f); exitCode(err) != 3 {
			t.Errorf("%s: exit %d (%v), want 3", name, exitCode(err), err)
		}
	}

	t.Setenv("MONOAGENT_API_MAX_CONCURRENT", "lots")
	db := testdb.Open(t)
	if _, err := newAPIRuntime(db.DB, Flags{}, func(string, ...any) {}); exitCode(err) != 3 {
		t.Errorf("a bad MONOAGENT_API_MAX_CONCURRENT: exit %d (%v), want 3", exitCode(err), err)
	}

	t.Setenv("MONOAGENT_API_MAX_CONCURRENT", "")
	t.Setenv("MONOAGENT_API_CONTEXT_CONFINEMENT", "nope")
	if _, err := newAPIRuntime(db.DB, Flags{}, func(string, ...any) {}); exitCode(err) != 3 {
		t.Errorf("a bad MONOAGENT_API_CONTEXT_CONFINEMENT: exit %d (%v), want 3", exitCode(err), err)
	}
}

func TestAPIRuntimePolicyDefaultsAndOverride(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Policy("127.0.0.1:9322").String(); got != "any" {
		t.Errorf("loopback default: %s", got)
	}
	if got := rt.Policy("0.0.0.0:9443").String(); got != "chat-only" {
		t.Errorf("off-loopback default: %s", got)
	}

	rt, err = newAPIRuntimeForTest(t, Flags{confinement: "sandboxed"})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Policy("127.0.0.1:9322").String() != "sandboxed" || rt.Policy("0.0.0.0:9443").String() != "sandboxed" {
		t.Error("an explicit confinement must apply to every listener")
	}
}

// A key created with --context is held to chat-only unless the operator raises
// it with --context-confinement, and never above the listener's own policy.
func TestAPIRuntimeContextConfinement(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Policy("127.0.0.1:9322").ForContextKey().String(); got != "chat-only" {
		t.Errorf("a context key on a loopback listener by default: %s, want chat-only", got)
	}
	if got := rt.contextReport(); got != "chat-only" {
		t.Errorf("contextReport = %q, want chat-only", got)
	}

	rt, err = newAPIRuntimeForTest(t, Flags{contextConfinement: "sandboxed"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Policy("127.0.0.1:9322").ForContextKey().String(); got != "sandboxed" {
		t.Errorf("--context-confinement sandboxed on loopback: %s", got)
	}
	if got := rt.Policy("0.0.0.0:9443").ForContextKey().String(); got != "chat-only" {
		t.Errorf("a chat-only network listener must stay chat-only for a context key: %s", got)
	}
	if got := rt.contextReport(); got != "sandboxed" {
		t.Errorf("contextReport = %q, want sandboxed", got)
	}
}

// The auto model picks among chat-only models unless the operator raises it with
// --auto-confinement (or MONOAGENT_API_AUTO_CONFINEMENT), and never above the
// listener's own policy.
func TestAPIRuntimeAutoConfinement(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Policy("127.0.0.1:9322").ForAuto().String(); got != "chat-only" {
		t.Errorf("auto on a loopback listener by default: %s, want chat-only", got)
	}
	if got := rt.autoReport(); got != "chat-only" {
		t.Errorf("autoReport = %q, want chat-only", got)
	}

	rt, err = newAPIRuntimeForTest(t, Flags{autoConfinement: "sandboxed"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Policy("127.0.0.1:9322").ForAuto().String(); got != "sandboxed" {
		t.Errorf("--auto-confinement sandboxed on loopback: %s", got)
	}
	if got := rt.Policy("0.0.0.0:9443").ForAuto().String(); got != "chat-only" {
		t.Errorf("a chat-only network listener must stay chat-only for auto: %s", got)
	}
	if got := rt.autoReport(); got != "sandboxed" {
		t.Errorf("autoReport = %q, want sandboxed", got)
	}

	// The environment is read when the flag is not given.
	t.Setenv("MONOAGENT_API_AUTO_CONFINEMENT", "any")
	rt, err = newAPIRuntime(testdb.Open(t).DB, Flags{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Policy("127.0.0.1:9322").ForAuto().String(); got != "any" {
		t.Errorf("MONOAGENT_API_AUTO_CONFINEMENT=any on loopback: %s", got)
	}

	if _, err := newAPIRuntimeForTest(t, Flags{autoConfinement: "nope"}); exitCode(err) != 3 {
		t.Errorf("a bad --auto-confinement must be invalid input, got exit %d (%v)", exitCode(err), err)
	}
}

// --max-concurrent 0 (as opposed to leaving the flag out) is an explicit
// request for no turns at all, so it is refused rather than read as the default.
func TestMaxConcurrentFlagRemembersWhetherItWasGiven(t *testing.T) {
	var f Flags
	cmd := &cobra.Command{Use: "x"}
	f.Bind(cmd)
	if err := cmd.ParseFlags([]string{"--max-concurrent", "0"}); err != nil {
		t.Fatal(err)
	}
	if !f.maxConcurrentSet || f.maxConcurrent != 0 {
		t.Fatalf("an explicit 0 must be remembered as given: %+v", f)
	}
	var g Flags
	cmd2 := &cobra.Command{Use: "y"}
	g.Bind(cmd2)
	if err := cmd2.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	if g.maxConcurrentSet {
		t.Error("a flag that was left out must not count as given")
	}
}

func TestMaxConcurrentEnvironmentIsBoundedToo(t *testing.T) {
	t.Setenv("MONOAGENT_API_MAX_CONCURRENT", "100000")
	db := testdb.Open(t)
	if _, err := newAPIRuntime(db.DB, Flags{}, func(string, ...any) {}); exitCode(err) != 3 {
		t.Errorf("an absurd MONOAGENT_API_MAX_CONCURRENT: exit %d (%v), want 3", exitCode(err), err)
	}
}

// drain must stop the dedicated listener itself: a command whose main bind failed
// never cancels the context the listener was started with.
func TestDrainStopsTheDedicatedListenerWithoutItsContextEnding(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{v1Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := rt.StartV1(context.Background()) // a context that never ends
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { rt.Drain(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("drain waited for a listener whose context never ends")
	}
	if resp, err := http.Get("http://" + addr + "/health"); err == nil {
		resp.Body.Close()
		t.Error("the dedicated listener still answers after drain")
	}
}

func TestStartV1ServesOnlyV1AndStopsWithItsContext(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{v1Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := rt.StartV1(ctx)
	if err != nil || addr == "" || strings.HasSuffix(addr, ":0") {
		t.Fatalf("startV1 = %q, %v; want the bound address", addr, err)
	}

	status := func(path string) int {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := status("/health"); got != 200 {
		t.Errorf("/health: %d", got)
	}
	if got := status("/v1/models"); got != 401 {
		t.Errorf("/v1/models without a key: %d, want 401", got)
	}
	if got := status("/workflows"); got != 404 {
		t.Errorf("/workflows on the dedicated listener: %d, want 404", got)
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get("http://" + addr + "/health"); err != nil {
			return // the listener is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the dedicated listener kept serving after its context ended")
}

func TestStartV1IsANoOpWithoutAnAddress(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if addr, err := rt.StartV1(context.Background()); addr != "" || err != nil {
		t.Fatalf("startV1 with no address = %q, %v", addr, err)
	}
}

func TestStartV1OffLoopbackNeedsATLSCertificate(t *testing.T) {
	// Off-loopback the listener is TLS only: with no certificate configured a
	// self-signed one is generated and cached, never plaintext.
	rt, err := newAPIRuntimeForTest(t, Flags{v1Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENT_API_TLS_CERT", "")
	t.Setenv("MONOAGENT_API_TLS_KEY", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := rt.StartV1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port := addr[strings.LastIndex(addr, ":")+1:]
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:" + port + "/health"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("an off-loopback listener answered plain HTTP")
		}
	}
}

// The daemon's single ExtraRoutes slot carries both: losing the org receiver
// would break every automation role, and /v1 must never appear off-loopback.
func TestDaemonRoutesKeepTheOrgReceiverAndMountV1OnlyOnLoopback(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	receiver := func(mux *http.ServeMux) {
		mux.HandleFunc("GET /org-endpoint/x", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "receiver") })
	}
	codeOf := func(addr, path string) int {
		mux := http.NewServeMux()
		DaemonRoutes(receiver, rt, addr)(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	for _, addr := range []string{"127.0.0.1:9322", "0.0.0.0:9322"} {
		if got := codeOf(addr, "/org-endpoint/x"); got != http.StatusOK {
			t.Errorf("%s: the org receiver answers %d, want 200: it must not be lost", addr, got)
		}
	}
	if got := codeOf("127.0.0.1:9322", "/v1/models"); got != http.StatusUnauthorized {
		t.Errorf("loopback: /v1/models answers %d, want 401 (mounted, asks for a key)", got)
	}
	if got := codeOf("0.0.0.0:9322", "/v1/models"); got != http.StatusNotFound {
		t.Errorf("off-loopback: /v1/models answers %d, want 404 (never mounted)", got)
	}
}

func TestStartV1OffLoopbackServesHTTPSWithTheGeneratedCertificate(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{v1Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENT_API_TLS_CERT", "")
	t.Setenv("MONOAGENT_API_TLS_KEY", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := rt.StartV1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port := addr[strings.LastIndex(addr, ":")+1:]
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // the certificate is self-signed
	for path, want := range map[string]int{"/health": 200, "/v1/models": 401, "/workflows": 404} {
		resp, err := client.Get("https://127.0.0.1:" + port + path)
		if err != nil {
			t.Fatalf("GET %s over TLS: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s over TLS: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestDrainWaitsForTheDedicatedListenerToStop(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{v1Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := rt.StartV1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { rt.Drain(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("drain did not return after the listener's context ended")
	}
	if resp, err := http.Get("http://" + addr + "/health"); err == nil {
		resp.Body.Close()
		t.Error("the dedicated listener still answers after drain")
	}
}

func TestConfinementReportOnlyForListenersThatServeV1(t *testing.T) {
	rt, err := newAPIRuntimeForTest(t, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		addr      string
		dedicated bool
		want      string
	}{
		{"127.0.0.1:9322", false, "any"},
		{"0.0.0.0:9322", false, ""}, // an off-loopback main listener does not serve /v1
		{"0.0.0.0:9443", true, "chat-only"},
		{"", true, ""},
	} {
		if got := rt.confinementReport(c.addr, c.dedicated); got != c.want {
			t.Errorf("confinementReport(%q, dedicated=%v) = %q, want %q", c.addr, c.dedicated, got, c.want)
		}
	}
}
