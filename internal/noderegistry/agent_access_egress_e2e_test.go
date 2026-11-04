package noderegistry

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A grant that names hosts holds a secret to them for every node type with
// destination keys, not only the http nodes (the 6i fix): a step of an
// unapproved workflow that gets the secret from the run's data and names a
// host of its own is refused before it runs.

const sendSecret = "vsecret-777"

type sendEnv struct {
	ctx   context.Context
	db    *sql.DB
	store *workflow.SQLiteWorkflowStore
	reg   *workflow.NodeTypeRegistry
}

func newSendEnv(t *testing.T, g secrets.AccessGrant) *sendEnv {
	t.Helper()
	keyring.MockInit()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "send.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), d.DB), "default")
	if _, err := secrets.Add(ctx, d.DB, "default", "secret", "vs", map[string]string{"secret": sendSecret}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(ctx, d.DB, "default", g); err != nil {
		t.Fatal(err)
	}
	return &sendEnv{ctx: ctx, db: d.DB, store: workflow.NewSQLiteWorkflowStore(d.DB), reg: workflow.NewNodeTypeRegistry()}
}

// reader is the step that reads the secret (mcp.call checks its destination
// at the send, so a host-bound grant lets it read) and passes it on.
type reader struct{}

func (reader) Type() string { return "mcp.call" }
func (reader) Execute(_ context.Context, _ workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(config)}}}, nil
}

// spy is a node type with the real type's destination keys that records
// that it ran.
type spy struct {
	typ  string
	keys [][]string
	ran  *[]string
}

func (s spy) Type() string                          { return s.typ }
func (s spy) CredentialDestinationKeys() [][]string { return s.keys }
func (s spy) Execute(_ context.Context, in workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	*s.ran = append(*s.ran, s.typ)
	return []workflow.NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// run runs the never-approved chain reader -> sender once.
func (e *sendEnv) run(t *testing.T, id, senderType string, cfg map[string]interface{}) error {
	t.Helper()
	e.reg.Register("mcp.call", func() workflow.NodeExecutor { return reader{} })
	return e.runWF(t, &workflow.Workflow{ID: id, Name: id, ProfileID: "default",
		Nodes: []workflow.WorkflowNode{
			{ID: id + "-t", WorkflowID: id, Type: "trigger.schedule", Name: "Every hour"},
			{ID: id + "-r", WorkflowID: id, Type: "mcp.call", Name: "Reader", Config: map[string]interface{}{"v": "@secret:vs"}},
			{ID: id + "-s", WorkflowID: id, Type: senderType, Name: "Sender", Config: cfg},
		},
		Connections: []workflow.WorkflowConnection{
			{ID: id + "-c1", WorkflowID: id, SourceNodeID: id + "-t", SourceHandle: "main", TargetNodeID: id + "-r", TargetHandle: "main"},
			{ID: id + "-c2", WorkflowID: id, SourceNodeID: id + "-r", SourceHandle: "main", TargetNodeID: id + "-s", TargetHandle: "main"},
		}})
}

func (e *sendEnv) runWF(t *testing.T, wf *workflow.Workflow) error {
	t.Helper()
	id := wf.ID
	if err := e.store.CreateWorkflow(e.ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveWorkflowNodes(e.ctx, id, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveWorkflowConnections(e.ctx, id, wf.Connections); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.store.GetWorkflow(e.ctx, id)
	dag, err := workflow.BuildDAG(cur.Nodes, cur.Connections)
	if err != nil {
		return err
	}
	exec := &workflow.WorkflowExecution{ID: uuid.NewString(), WorkflowID: id, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: id + "-t"}
	if err := e.store.CreateExecution(e.ctx, exec); err != nil {
		t.Fatal(err)
	}
	return workflow.RunExecution(e.ctx, exec, cur, dag, e.reg, e.store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
}

// noSendGuard lets the real types run: the exec guard would hold an agent's
// send for approval, which is not what these tests are about.
func noSendGuard(t *testing.T) {
	t.Helper()
	workflow.SetExecGuard(nil)
	t.Cleanup(func() { workflow.SetExecGuard(hilsend.Guard) })
}

// hostValue sets every host-like key of keys to host (a port or a vendor
// name is no host), nested keys ("embedding.base_url") inside their block.
func hostConfig(keys [][]string, host string) map[string]interface{} {
	cfg := map[string]interface{}{"data": "{{ $json.v }}"}
	for _, group := range keys {
		for _, k := range group {
			if k == "credential_platform" || k == "port" || strings.HasSuffix(k, "_port") {
				continue
			}
			if block, key, nested := strings.Cut(k, "."); nested {
				m, _ := cfg[block].(map[string]interface{})
				if m == nil {
					m = map[string]interface{}{}
					cfg[block] = m
				}
				m[key] = host
				continue
			}
			cfg[k] = host
		}
	}
	return cfg
}

// expectedHostKeyedTypes are the registered types with host-like destination
// keys that do not check at the send themselves, exactly (the other four types
// with destination keys: http.request and http.graphql check each hop at the
// send; agent.ask and the aisub types name only a credential_platform).
var expectedHostKeyedTypes = []string{
	"comm.email_read", "comm.email_send", "db.ldap", "db.snowflake", "http.ftp", "http.ssh",
	"queue.amqp", "queue.kafka", "queue.mqtt", "queue.rabbitmq",
	"service.jira", "service.mastodon", "service.salesforce", "service.shopify",
	"vector.embed", "vector.embeddings", "vector.store",
}

// Every registered type with destination keys, at the engine level (the
// network is faked: a spy with the type's keys records that Execute ran).
func TestHostBoundGrantHoldsEveryRegisteredTypeWithDestinationKeys(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	var types []string
	keysOf := map[string][][]string{}
	for _, nodeType := range real.Types() {
		factory, _ := real.Get(nodeType)
		d, ok := factory().(workflow.CredentialDestinations)
		if !ok || len(d.CredentialDestinationKeys()) == 0 {
			continue
		}
		if secrets.ChecksHostAtSend(nodeType) {
			continue // http.request and http.graphql: checked at the send, below
		}
		onlyVendor := true
		for _, g := range d.CredentialDestinationKeys() {
			for _, k := range g {
				if k != "credential_platform" {
					onlyVendor = false
				}
			}
		}
		if onlyVendor {
			continue // names a model vendor, not a host
		}
		types = append(types, nodeType)
		keysOf[nodeType] = d.CredentialDestinationKeys()
	}
	sort.Strings(types)
	if !reflect.DeepEqual(types, expectedHostKeyedTypes) {
		t.Fatalf("the registered types with host-like destination keys changed: now %v, expected %v; a type that gains destination keys must be held to the grant's hosts (add it to expectedHostKeyedTypes after checking the table below covers it), one that loses them leaves the check", types, expectedHostKeyedTypes)
	}
	t.Logf("%d types with destination keys: %v", len(types), types)
	for _, nodeType := range types {
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		var ran []string
		e.reg.Register(nodeType, func() workflow.NodeExecutor { return spy{typ: nodeType, keys: keysOf[nodeType], ran: &ran} })
		id := "wf-" + strings.ReplaceAll(nodeType, ".", "-")
		err := e.run(t, id, nodeType, hostConfig(keysOf[nodeType], "evil.example"))
		if errors.Is(err, workflow.ErrSubNodeWiring) {
			// An AI sub-node type: held where its root resolves it
			// (TestHostBoundGrantHoldsASubNodeWithDestinationKeys).
			t.Logf("%s is a sub-node type: covered through its root", nodeType)
			continue
		}
		if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: a host outside the grant was not refused: %v", nodeType, err)
		}
		if len(ran) != 0 {
			t.Errorf("%s: Execute ran", nodeType)
		}
		// The same run with a host the grant names goes through.
		e2 := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		ran = nil
		e2.reg.Register(nodeType, func() workflow.NodeExecutor { return spy{typ: nodeType, keys: keysOf[nodeType], ran: &ran} })
		// A send an agent's run makes is held for approval before it runs
		// (comm.email_send): no refusal by the grant is what counts there.
		err = e2.run(t, id, nodeType, hostConfig(keysOf[nodeType], "good.example"))
		if err != nil || (len(ran) != 1 && nodeType != "comm.email_send") {
			t.Errorf("%s: a granted host: %v (ran %d)", nodeType, err, len(ran))
		}
		// And an --any-host grant holds nothing.
		e3 := newSendEnv(t, secrets.AccessGrant{AnyHost: true})
		ran = nil
		e3.reg.Register(nodeType, func() workflow.NodeExecutor { return spy{typ: nodeType, keys: keysOf[nodeType], ran: &ran} })
		if err := e3.run(t, id, nodeType, hostConfig(keysOf[nodeType], "evil.example")); err != nil || (len(ran) != 1 && nodeType != "comm.email_send") {
			t.Errorf("%s: --any-host: %v (ran %d)", nodeType, err, len(ran))
		}
	}
}

// tcpSink records what a client sends it, then closes.
type tcpSink struct {
	l    net.Listener
	mu   sync.Mutex
	got  bytes.Buffer
	conn int
}

func newTCPSink(t *testing.T) *tcpSink {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	s := &tcpSink{l: l}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s.mu.Lock()
				s.conn++
				s.mu.Unlock()
				c.SetReadDeadline(time.Now().Add(time.Second))
				buf := make([]byte, 8192)
				n, _ := c.Read(buf)
				s.mu.Lock()
				s.got.Write(buf[:n])
				s.mu.Unlock()
			}()
		}
	}()
	return s
}

func (s *tcpSink) port() int { return s.l.Addr().(*net.TCPAddr).Port }
func (s *tcpSink) seen() (conns int, secret bool) {
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn, bytes.Contains(s.got.Bytes(), []byte(sendSecret))
}

// End to end with the real nodes and a loopback listener: with a grant
// that names other hosts, nothing reaches the listener; with one that names
// it, the secret does (the seam works).
func TestHostBoundGrantKeepsTheSecretOffTheListenerOfARealNode(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	for _, c := range []struct {
		typ string
		cfg func(port int) map[string]interface{}
	}{
		{"queue.mqtt", func(p int) map[string]interface{} {
			return map[string]interface{}{"host": "127.0.0.1", "port": p, "username": "u", "password": "{{ $json.v }}", "topic": "t", "message": "x"}
		}},
		{"db.ldap", func(p int) map[string]interface{} {
			return map[string]interface{}{"hostname": "127.0.0.1", "port": p, "bind_dn": "cn=x", "bind_password": "{{ $json.v }}", "operation": "delete", "dn": "cn=y"}
		}},
	} {
		factory, ok := real.Get(c.typ)
		if !ok {
			t.Fatalf("%s is not registered", c.typ)
		}
		bound := newTCPSink(t)
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		e.reg.Register(c.typ, factory)
		err := e.run(t, "wf-e2e-"+strings.ReplaceAll(c.typ, ".", "-"), c.typ, c.cfg(bound.port()))
		if conns, leaked := bound.seen(); conns != 0 || leaked {
			t.Errorf("%s: the sink saw %d connections, secret=%v (run: %v)", c.typ, conns, leaked, err)
		}
		if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: not refused by the grant: %v", c.typ, err)
		}
		// Control: the grant names the host, and the real node reaches the sink.
		open := newTCPSink(t)
		e2 := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"127.0.0.1"}})
		e2.reg.Register(c.typ, factory)
		_ = e2.run(t, "wf-e2e-ok-"+strings.ReplaceAll(c.typ, ".", "-"), c.typ, c.cfg(open.port()))
		if conns, leaked := open.seen(); conns == 0 || !leaked {
			t.Errorf("%s control: the listener saw %d connections, secret=%v: the seam does not carry the secret", c.typ, conns, leaked)
		}
	}
}

// The http nodes check each hop at the send (unchanged by the 6i fix): the
// listener of a host the grant does not name gets nothing.
func TestHostBoundGrantKeepsTheSecretOffTheListenerOfTheHTTPNodes(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	for typ, cfg := range map[string]func(u string) map[string]interface{}{
		"http.request": func(u string) map[string]interface{} {
			return map[string]interface{}{"url": u, "method": "POST", "body": "{{ $json.v }}"}
		},
		"http.graphql": func(u string) map[string]interface{} {
			return map[string]interface{}{"endpoint": u, "query": "{ a }", "variables": map[string]interface{}{"k": "{{ $json.v }}"}}
		},
	} {
		var mu sync.Mutex
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits++
			mu.Unlock()
		}))
		factory, _ := real.Get(typ)
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		e.reg.Register(typ, factory)
		err := e.run(t, "wf-"+strings.ReplaceAll(typ, ".", "-"), typ, cfg(srv.URL))
		mu.Lock()
		n := hits
		mu.Unlock()
		srv.Close()
		if n != 0 || err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: hits=%d err=%v", typ, n, err)
		}
	}
}

// No destination key of a registered type is held back as a per-item field:
// a field the node resolves for itself at Execute (db's resolveForItem, an API
// package's itemParams) would not be seen by the engine's check. The http
// nodes and mcp.call check at the send.
func TestNoDestinationKeyIsResolvedByTheNodeItself(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	for _, nodeType := range real.Types() {
		factory, _ := real.Get(nodeType)
		exec := factory()
		d, ok := exec.(workflow.CredentialDestinations)
		r, held := exec.(workflow.PerItemConfigResolver)
		if !ok || !held || secrets.ChecksHostAtSend(nodeType) {
			continue
		}
		heldSet := map[string]bool{}
		for _, f := range r.PerItemConfigFields() {
			heldSet[f] = true
		}
		for _, g := range d.CredentialDestinationKeys() {
			for _, k := range g {
				if heldSet[k] {
					t.Errorf("%s: destination key %q is a per-item field the node resolves itself at Execute, past the engine's check", nodeType, k)
				}
			}
		}
	}
}

// twoItems reads the secret and emits two items: one naming a granted host,
// one the host config["h2"] names.
type twoItems struct{}

func (twoItems) Type() string { return "mcp.call" }
func (twoItems) Execute(_ context.Context, _ workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"v": config["v"], "h": "good.example"}),
		workflow.NewItem(map[string]interface{}{"v": config["v"], "h": config["h2"]}),
	}}}, nil
}

// A loop re-runs its body once per batch; the second iteration naming an
// outside host is refused before the node runs, the first goes through.
func TestHostBoundGrantHoldsALoopIteration(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	loop, _ := real.Get(workflow.LoopNodeType)
	for host, refused := range map[string]bool{"evil.example": true, "good.example": false} {
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		var ran []string
		e.reg.Register(workflow.LoopNodeType, loop)
		e.reg.Register("mcp.call", func() workflow.NodeExecutor { return twoItems{} })
		e.reg.Register("http.ssh", func() workflow.NodeExecutor { return spy{typ: "http.ssh", keys: sshKeys, ran: &ran} })
		conn := func(id, from, handle, to string) workflow.WorkflowConnection {
			return workflow.WorkflowConnection{ID: id, WorkflowID: "wf-loop", SourceNodeID: from, SourceHandle: handle, TargetNodeID: to, TargetHandle: "main"}
		}
		wf := &workflow.Workflow{ID: "wf-loop", Name: "loop", ProfileID: "default",
			Nodes: []workflow.WorkflowNode{
				{ID: "wf-loop-t", WorkflowID: "wf-loop", Type: "trigger.schedule", Name: "T"},
				{ID: "r", WorkflowID: "wf-loop", Type: "mcp.call", Name: "Reader", Config: map[string]interface{}{"v": "@secret:vs", "h2": host}},
				{ID: "s", WorkflowID: "wf-loop", Type: workflow.LoopNodeType, Name: "Loop", Config: map[string]interface{}{"batch_size": 1}},
				{ID: "b", WorkflowID: "wf-loop", Type: "http.ssh", Name: "Body", Config: map[string]interface{}{"host": "{{ $json.h }}", "data": "{{ $json.v }}"}},
			},
			Connections: []workflow.WorkflowConnection{conn("c1", "wf-loop-t", "main", "r"), conn("c2", "r", "main", "s"), conn("c3", "s", "loop", "b"), conn("c4", "b", "main", "s")}}
		err := e.runWF(t, wf)
		if refused != errors.Is(err, secrets.ErrHostNotAllowed) && refused != (err != nil && strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error())) {
			t.Errorf("loop with host %s: %v", host, err)
		}
		want := 2
		if refused {
			want = 1
		}
		if len(ran) != want {
			t.Errorf("loop with host %s: Execute ran %d times, want %d", host, len(ran), want)
		}
	}
}

var sshKeys = [][]string{{"host"}, {"port"}}

// End to end, the nine database types with their real nodes: a connection
// string that names a loopback host the grant does not name never connects
// (the DSN's user carries the secret, which a postgres-wire startup message
// and a redis AUTH put on the wire); with the host granted the same node does
// connect, which proves the sink sees it.
func TestHostBoundGrantKeepsTheNineDatabaseNodesOffAHostOutsideTheGrant(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	dsn := map[string]func(port int) map[string]interface{}{
		"db.postgres":    pgWire("postgres"),
		"db.cratedb":     pgWire("postgres"),
		"db.questdb":     pgWire("postgres"),
		"db.timescaledb": pgWire("postgres"),
		"db.mysql": func(p int) map[string]interface{} {
			return map[string]interface{}{"connection_string": "{{ $json.v }}:x@tcp(127.0.0.1:" + strconv.Itoa(p) + ")/db", "query": "select 1"}
		},
		"db.mssql": func(p int) map[string]interface{} {
			return map[string]interface{}{"connection_string": "sqlserver://{{ $json.v }}:x@127.0.0.1:" + strconv.Itoa(p) + "?database=db", "query": "select 1"}
		},
		"db.oracle": func(p int) map[string]interface{} {
			return map[string]interface{}{"connection_string": "oracle://{{ $json.v }}:x@127.0.0.1:" + strconv.Itoa(p) + "/svc", "query": "select 1 from dual"}
		},
		"db.mongodb": func(p int) map[string]interface{} {
			return map[string]interface{}{"connection_string": "mongodb://{{ $json.v }}:x@127.0.0.1:" + strconv.Itoa(p) + "/db?serverSelectionTimeoutMS=1500&connectTimeoutMS=1500", "operation": "find", "database": "db", "collection": "c"}
		},
		"db.redis": func(p int) map[string]interface{} {
			return map[string]interface{}{"connection_string": "redis://:{{ $json.v }}@127.0.0.1:" + strconv.Itoa(p) + "/0", "operation": "get", "key": "k"}
		},
	}
	for typ, cfg := range dsn {
		factory, ok := real.Get(typ)
		if !ok {
			t.Fatalf("%s is not registered", typ)
		}
		id := "wf-db-" + strings.ReplaceAll(typ, ".", "-")
		bound := newTCPSink(t)
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		e.reg.Register(typ, factory)
		err := e.run(t, id, typ, cfg(bound.port()))
		if conns, leaked := bound.seen(); conns != 0 || leaked {
			t.Errorf("%s: the sink outside the grant saw %d connections, secret=%v (run: %v)", typ, conns, leaked, err)
		}
		if err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: not refused by the grant: %v", typ, err)
		}
		open := newTCPSink(t)
		e2 := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"127.0.0.1"}})
		e2.reg.Register(typ, factory)
		cerr := e2.run(t, id, typ, cfg(open.port()))
		if conns, _ := open.seen(); conns == 0 {
			t.Errorf("%s control: the node did not reach a sink its host is granted for (run: %v)", typ, cerr)
		}
	}
}

func pgWire(scheme string) func(port int) map[string]interface{} {
	return func(p int) map[string]interface{} {
		return map[string]interface{}{"connection_string": scheme + "://{{ $json.v }}:x@127.0.0.1:" + strconv.Itoa(p) + "/db?sslmode=disable", "query": "select 1"}
	}
}

// The URL-fetch type rss_read: a URL with the secret in its query, to a loopback
// server outside the grant, is never requested.
func TestHostBoundGrantKeepsTheURLFetchNodesOffAHostOutsideTheGrant(t *testing.T) {
	noSendGuard(t)
	real := Build(nil)
	// ai.read_page and ai.extract_page refuse a loopback address themselves:
	// they are covered at the engine level only.
	for typ, field := range map[string]string{"system.rss_read": "url"} {
		var mu sync.Mutex
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits++
			mu.Unlock()
		}))
		factory, ok := real.Get(typ)
		if !ok {
			t.Fatalf("%s is not registered", typ)
		}
		cfg := map[string]interface{}{field: srv.URL + "/feed?key={{ $json.v }}"}
		e := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
		e.reg.Register(typ, factory)
		err := e.run(t, "wf-url-"+strings.ReplaceAll(typ, ".", "-"), typ, cfg)
		mu.Lock()
		n := hits
		mu.Unlock()
		if n != 0 || err == nil || !strings.Contains(err.Error(), secrets.ErrHostNotAllowed.Error()) {
			t.Errorf("%s: hits=%d err=%v", typ, n, err)
		}
		e2 := newSendEnv(t, secrets.AccessGrant{Hosts: []string{"127.0.0.1"}})
		e2.reg.Register(typ, factory)
		cerr := e2.run(t, "wf-url-ok-"+strings.ReplaceAll(typ, ".", "-"), typ, cfg)
		mu.Lock()
		n = hits
		mu.Unlock()
		srv.Close()
		if n == 0 {
			t.Errorf("%s control: the node did not request a URL its host is granted for (run: %v)", typ, cerr)
		}
	}
}
