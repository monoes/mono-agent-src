package node

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A process inside a coder turn that cleared its markers must not read the vault or a saved connection through
// `node run` any more than a marked agent does: no @secret reference resolves without the profile's agent access,
// with it the value is usable only as a marked agent could use it (and audited), and a saved connection's secret
// goes only to the hosts the connection names.

const inTurnSecret = "vsecret-in-turn-4242"

type secretRun struct {
	out    string
	err    error
	bodies []string
	audit  int
}

// runWithSecret runs `node run <nodeType>` as caller c with an @secret reference to the seeded entry in
// config field (a body, a header, a GraphQL query), against a loopback listener, under agent-access grant
// (nil: the profile's agent access is off).
func runWithSecret(t *testing.T, c callerContext, nodeType string, config func(url string) string, grant *secrets.AccessGrant) secretRun {
	t.Helper()
	dbPath := migratedTestDB(t) // clears the markers: set the caller up after it
	c.setup(t)
	seedVaultSecret(t, dbPath, "vs", inTurnSecret)
	srv, got := requestRecorder(t)
	d := testDeps(dbPath, "default", false)
	if grant != nil {
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := secrets.SetAgentAccessGrant(context.Background(), db.DB, "default", *grant); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runNodeSubcmdStdin(t, d, config(srv.URL), nodeType)
	db, derr := storage.NewDatabase(dbPath)
	if derr != nil {
		t.Fatal(derr)
	}
	defer db.Close()
	rows, aerr := secrets.AgentAccessAudit(context.Background(), db.DB, "default")
	if aerr != nil {
		t.Fatal(aerr)
	}
	return secretRun{out: out, err: err, bodies: got(), audit: len(rows)}
}

func (r secretRun) shown() string {
	if r.err != nil {
		return r.out + r.err.Error()
	}
	return r.out
}

// requestRecorder is a listener that keeps, per request, its Authorization header and body.
func requestRecorder(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization")+"|"+string(b))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// contextsFor: every caller for the case that names the node's main path, only a process in a turn (and the marked
// agent it is compared with) for the others: the node cannot tell the callers apart, only the verdict can.
func contextsFor(name string) []callerContext {
	all := callerContexts()
	if name == secretNodes[0].name {
		return all
	}
	return []callerContext{all[0], all[1], all[5]}
}

var secretNodes = []struct {
	name, nodeType string
	config         func(url string) string
}{
	{"http.request body", "http.request", func(u string) string {
		return `{"config":{"url":"` + u + `","method":"POST","body":"@secret:vs"}}`
	}},
	{"http.request header", "http.request", func(u string) string {
		return `{"config":{"url":"` + u + `","method":"POST","body":"x","headers":{"Authorization":"@secret:vs"}}}`
	}},
	{"http.graphql query", "http.graphql", func(u string) string {
		return `{"config":{"url":"` + u + `","query":"@secret:vs"}}`
	}},
}

// With the profile's agent access off the value is in no request and no output, and the result is the marked
// agent's, in every caller that is not the user's terminal (the body reference is refused; a header or query
// reference stays the literal text).
func TestNodeRunInATurnResolvesNoSecretWithAgentAccessOff(t *testing.T) {
	for _, n := range secretNodes {
		marked := runWithSecret(t, callerContexts()[5], n.nodeType, n.config, nil)
		for _, c := range contextsFor(n.name) {
			if !c.agent {
				continue
			}
			t.Run(n.name+"/"+c.name, func(t *testing.T) {
				r := runWithSecret(t, c, n.nodeType, n.config, nil)
				if strings.Contains(strings.Join(r.bodies, "\n"), inTurnSecret) || strings.Contains(r.shown(), inTurnSecret) {
					t.Fatalf("the value got out: bodies %q, output %s", r.bodies, r.shown())
				}
				// exactly the marked agent's result: the reference stays unresolved or the run fails
				refused := func(r secretRun) bool { return strings.Contains(r.shown(), "is not read from the vault") }
				if refused(r) != refused(marked) || (r.err == nil) != (marked.err == nil) || strings.Join(r.bodies, "\n") != strings.Join(marked.bodies, "\n") {
					t.Fatalf("in-turn: %v refused=%v bodies %q; marked agent: %v refused=%v bodies %q", r.err, refused(r), r.bodies, marked.err, refused(marked), marked.bodies)
				}
				if r.audit != 0 {
					t.Fatalf("%d audit rows with access off", r.audit)
				}
			})
		}
	}
}

// The user's own terminal resolves it as before.
func TestNodeRunOperatorResolvesTheSecretUnchanged(t *testing.T) {
	op := callerContexts()[0]
	r := runWithSecret(t, op, "http.request", secretNodes[0].config, nil)
	if r.err != nil || len(r.bodies) != 1 || r.bodies[0] != "|"+inTurnSecret || r.audit != 0 {
		t.Fatalf("operator: %v %q audit %d\n%s", r.err, r.bodies, r.audit, r.out)
	}
}

// With the profile's agent access on for the loopback host, a process in a turn gets the marked agent's result:
// the same bytes reach the same host, audited, and the value is in no output.
func TestNodeRunInATurnWithAgentAccessIsTheMarkedAgentsResult(t *testing.T) {
	grant := &secrets.AccessGrant{Hosts: []string{"127.0.0.1"}}
	for _, n := range secretNodes {
		marked := runWithSecret(t, callerContexts()[5], n.nodeType, n.config, grant) // marked agent
		if n.name == "http.request body" && (marked.audit == 0 || len(marked.bodies) != 1) {
			t.Fatalf("the marked agent's run under the grant resolved nothing (audit %d, bodies %q): the test proves nothing", marked.audit, marked.bodies)
		}
		for _, c := range contextsFor(n.name) {
			if !c.agent {
				continue
			}
			t.Run(n.name+"/"+c.name, func(t *testing.T) {
				r := runWithSecret(t, c, n.nodeType, n.config, grant)
				if len(r.bodies) != len(marked.bodies) || r.audit != marked.audit || (r.err == nil) != (marked.err == nil) {
					t.Fatalf("in-turn: %v bodies %q audit %d; marked agent: %v bodies %q audit %d", r.err, r.bodies, r.audit, marked.err, marked.bodies, marked.audit)
				}
				if strings.Contains(r.shown(), inTurnSecret) {
					t.Fatalf("the value is in the output: %s", r.shown())
				}
			})
		}
	}
}

// --credential / config.credential_id: a saved connection's secret goes only to the hosts the connection names,
// from a process in a turn as from a marked agent; the value is in no output.
func TestNodeRunInATurnSendsAConnectionOnlyToItsHosts(t *testing.T) {
	for _, viaFlag := range []bool{true, false} {
		for _, c := range callerContexts() {
			if !c.agent {
				continue
			}
			name := c.name + "/credential_id"
			if viaFlag {
				name = c.name + "/--credential"
			}
			t.Run(name, func(t *testing.T) {
				dbPath := migratedTestDB(t)
				c.setup(t)
				srv, got := requestRecorder(t)
				d := testDeps(dbPath, "default", false)
				d.ResolveCredential = func(_ context.Context, _ *connections.Store, key, _ string) (map[string]interface{}, error) {
					if key != "api-conn" {
						return nil, errors.New("no such connection")
					}
					return map[string]interface{}{"access_token": inTurnSecret, "base_url": "https://api.example.test"}, nil
				}
				config := `{"url":"` + srv.URL + `","method":"POST","body":"{{ $credential.access_token }}"`
				var args []string
				if viaFlag {
					args = []string{"--credential", "api-conn"}
					config += `}`
				} else {
					config += `,"credential_id":"api-conn"}`
				}
				out, err := runNodeSubcmdStdin(t, d, `{"config":`+config+`}`, append([]string{"http.request"}, args...)...)
				if !errors.Is(err, workflow.ErrCredentialDestination) {
					t.Errorf("err = %v\n%s", err, out)
				}
				if b := got(); len(b) != 0 {
					t.Fatalf("the listener got %q", b)
				}
				if strings.Contains(out, inTurnSecret) || (err != nil && strings.Contains(err.Error(), inTurnSecret)) {
					t.Fatalf("the value is in the output: %s %v", out, err)
				}
			})
		}
	}
}
