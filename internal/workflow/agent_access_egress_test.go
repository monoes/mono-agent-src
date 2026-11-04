package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

// The egress check (CheckEgress): in a run that carries a secret read under a
// grant that names hosts, a node type whose destinations the engine derives
// ("checked") may send only to the grant's hosts, a type that does network
// I/O it cannot derive ("unchecked", the default) is refused, and a type with
// no network I/O ("none") runs. --any-host, no grant and approved workflows
// are unchanged.

// egressClasses installs the classification of the test node types.
func egressClasses(t *testing.T) {
	t.Helper()
	SetEgressClassifier(func(typ string) (Egress, bool) {
		switch typ {
		case "test.destprobe":
			return EgressChecked, true
		case "test.pure", "test.echoprobe", "test.toolroot":
			return EgressNone, true
		}
		return EgressUnchecked, false // test.unclassified, anything else
	})
	t.Cleanup(func() { SetEgressClassifier(nil) })
}

// destProbe is a checked node type with destination keys that records what
// Execute was given.
type destProbe struct {
	keys [][]string
	got  *[]map[string]interface{}
}

func (destProbe) Type() string                            { return "test.destprobe" }
func (p destProbe) CredentialDestinationKeys() [][]string { return p.keys }
func (p destProbe) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	*p.got = append(*p.got, config)
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(config)}}}, nil
}

// typedProbe records that a node of another type ran.
type typedProbe struct {
	typ string
	ran *int
}

func (p typedProbe) Type() string { return p.typ }
func (p typedProbe) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*p.ran++
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

var sshLikeKeys = [][]string{{"host"}, {"port"}}

// sendChain is the route the check closes: a step that may read the secret
// (http.request, which checks its destination at the send) puts it in the
// run's data, and a later step names a host of its own.
func (e *accessEnv) sendChain(id string, keys [][]string, dest map[string]interface{}) (*Workflow, *[]map[string]interface{}) {
	got := &[]map[string]interface{}{}
	e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
	e.reg.Register("test.destprobe", func() NodeExecutor { return destProbe{keys: keys, got: got} })
	cfg := map[string]interface{}{"data": "{{ $json.v }}"}
	for k, v := range dest {
		cfg[k] = v
	}
	return chain(id,
		step(id+"-r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}),
		step(id+"-d", "test.destprobe", "Sender", cfg)), got
}

func newHostBound(t *testing.T) *accessEnv {
	t.Helper()
	egressClasses(t)
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example", "münchen.example"}})
	return e
}

func TestCheckedTypeIsHeldToTheGrantsHosts(t *testing.T) {
	e := newHostBound(t)
	wf, got := e.sendChain("wf-out", sshLikeKeys, map[string]interface{}{"host": "evil.example", "port": 22})
	execID, err := e.run(t, wf, nil)
	if !errors.Is(err, secrets.ErrHostNotAllowed) {
		t.Errorf("a host outside the grant was not refused: %v", err)
	}
	if len(*got) != 0 || strings.Contains(e.stored(t, execID), valVS) {
		t.Errorf("the secret reached the node or a record: %v", *got)
	}
	e = newHostBound(t)
	wf2, got2 := e.sendChain("wf-in", sshLikeKeys, map[string]interface{}{"host": "GOOD.example", "port": 22})
	if _, err := e.run(t, wf2, nil); err != nil || len(*got2) != 1 || (*got2)[0]["data"] != valVS {
		t.Fatalf("a granted host: %v %v", err, *got2)
	}
	if rows, err := secrets.AgentAccessAudit(e.ctx, e.db.DB, "default"); err != nil || len(rows) != 1 || rows[0].Secret != "vs" {
		t.Errorf("audit: %v %v", rows, err)
	}
}

// An unclassified type is unchecked: refused in a carrying run, and the
// refusal names the type and says an earlier step read the secret. A none type
// runs; so does an unchecked one in a run that read nothing, under --any-host,
// and in an approved workflow.
func TestUncheckedTypeIsRefusedInACarryingRun(t *testing.T) {
	e := newHostBound(t)
	ran := 0
	for _, typ := range []string{"test.unclassified", "test.pure"} {
		typ := typ
		e.reg.Register(typ, func() NodeExecutor { return typedProbe{typ, &ran} })
	}
	e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
	reader := step("rd-r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"})
	_, err := e.run(t, chain("wf-unc", reader, step("rd-u", "test.unclassified", "Other", nil)), nil)
	if !errors.Is(err, secrets.ErrHostNotAllowed) || !strings.Contains(err.Error(), "test.unclassified") || !strings.Contains(err.Error(), "an earlier step of the run read a secret") || ran != 0 {
		t.Fatalf("an unclassified type in a carrying run: %v (ran %d)", err, ran)
	}
	if _, err := e.run(t, chain("wf-pure", step("pu-r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}), step("pu-p", "test.pure", "Pure", nil)), nil); err != nil || ran != 1 {
		t.Errorf("a none type: %v (ran %d)", err, ran)
	}
	if _, err := e.run(t, chain("wf-nosecret", step("ns-u", "test.unclassified", "Other", nil)), nil); err != nil || ran != 2 {
		t.Errorf("a run that read no secret: %v (ran %d)", err, ran)
	}
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	if _, err := e.run(t, chain("wf-any", step("an-r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}), step("an-u", "test.unclassified", "Other", nil)), nil); err != nil || ran != 3 {
		t.Errorf("--any-host: %v (ran %d)", err, ran)
	}
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	wf := chain("wf-appr", step("ap-r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}), step("ap-u", "test.unclassified", "Other", nil))
	createSQLWorkflow(t, e.store, wf)
	if err := e.store.ApproveDefinition(e.ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.store.GetWorkflow(e.ctx, wf.ID)
	if _, err := e.runStored(t, cur); err != nil || ran != 4 {
		t.Errorf("an approved workflow: %v (ran %d)", err, ran)
	}
}

func TestEgressWordingSaysWhichStepReadTheSecret(t *testing.T) {
	e := newHostBound(t)
	ctx := secrets.AllowResolving(e.ctx, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: "w", ExecutionID: "x"})
	reader := secrets.WithAccessNode(ctx, "n1", "Reader")
	if _, err := secrets.Resolve(reader, e.db.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.CheckSendHost(reader, "evil.example"); err == nil || !strings.Contains(err.Error(), "this step read a secret") {
		t.Errorf("the step that read: %v", err)
	}
	if err := secrets.CheckSendHost(secrets.WithAccessNode(ctx, "n2", "Other"), "evil.example"); err == nil || !strings.Contains(err.Error(), "an earlier step of the run read a secret") {
		t.Errorf("another step: %v", err)
	}
}

// What a checked node names, however it is written: destination keys, a
// connection string's hosts, a URL value; spellings follow the grant's matching
// rules (secrets.HostOf).
func TestCheckEgressDestinations(t *testing.T) {
	e := newHostBound(t)
	ctx := secrets.AllowResolving(e.ctx, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: "w", ExecutionID: "x"})
	if _, err := secrets.Resolve(secrets.WithAccessNode(ctx, "n", "N"), e.db.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	ctx = secrets.WithAccessNode(ctx, "m", "M")
	cs := func(v string) map[string]interface{} { return map[string]interface{}{"connection_string": v} }
	hostKey := func(v interface{}) map[string]interface{} { return map[string]interface{}{"host": v} }
	for _, c := range []struct {
		name    string
		keys    [][]string
		cfg     map[string]interface{}
		allowed bool
	}{
		{"host", [][]string{{"host"}}, hostKey("good.example"), true},
		{"case", [][]string{{"host"}}, hostKey("GOOD.example"), true},
		{"trailing dot", [][]string{{"host"}}, hostKey("good.example."), true},
		{"host:port", [][]string{{"host"}}, hostKey("good.example:2222"), true},
		{"url", [][]string{{"host"}}, hostKey("https://GOOD.example.:8443/x?q=1"), true},
		{"userinfo of a granted host", [][]string{{"host"}}, hostKey("evil.example@good.example"), true},
		{"idn as granted", [][]string{{"host"}}, hostKey("münchen.example"), true},
		{"idn as punycode", [][]string{{"host"}}, hostKey("xn--mnchen-3ya.example"), false},
		{"granted host in userinfo", [][]string{{"host"}}, hostKey("good.example@evil.example"), false},
		{"url, granted host in userinfo", [][]string{{"host"}}, hostKey("https://good.example@evil.example/"), false},
		{"backslash before an @", [][]string{{"host"}}, hostKey("https://good.example\\@evil.example/"), false},
		{"list with an outside host", [][]string{{"brokers"}}, map[string]interface{}{"brokers": "good.example:9092,evil.example:9092"}, false},
		{"suffix trick", [][]string{{"host"}}, hostKey("good.example.evil.example"), false},
		{"outside host", [][]string{{"host"}}, hostKey("evil.example"), false},
		{"second alias of a group", [][]string{{"url", "base_url"}}, map[string]interface{}{"base_url": "https://evil.example"}, false},
		{"nested block", [][]string{{"embedding.base_url"}}, map[string]interface{}{"embedding": map[string]interface{}{"base_url": "https://evil.example"}}, false},
		{"expression left in a host", [][]string{{"host"}}, hostKey("{{ $json.h }}"), false},
		{"decimal IP in a host key", [][]string{{"host"}}, hostKey("2130706433"), false},
		{"hex IP in a host key", [][]string{{"host"}}, hostKey("0x7f000001"), false},
		{"octal IP in a host key", [][]string{{"host"}}, hostKey("0177.0.0.1"), false},
		{"number in a host key", [][]string{{"host"}}, hostKey(2130706433), false},
		{"port key with digits", [][]string{{"host"}, {"port"}}, map[string]interface{}{"host": "good.example", "port": 5432}, true},
		{"_port key with digits", [][]string{{"imap_host"}, {"imap_port"}}, map[string]interface{}{"imap_host": "good.example", "imap_port": "993"}, true},
		{"port key naming a host", [][]string{{"host"}, {"port"}}, map[string]interface{}{"host": "good.example", "port": "evil.example"}, false},
		{"vendor name", [][]string{{"credential_platform"}}, map[string]interface{}{"credential_platform": "openai"}, true},
		{"unset key", [][]string{{"host"}}, map[string]interface{}{}, true},
		// connection strings: every host of the forms the db nodes take
		{"postgres url", nil, cs("postgres://u:p@good.example:5432/db"), true},
		{"postgres url outside", nil, cs("postgres://u:p@evil.example:5432/db"), false},
		{"postgres url, host in the query", nil, cs("postgres://u:p@good.example/db?host=evil.example"), false},
		{"postgres key=value", nil, cs("host=good.example port=5432 user=u"), true},
		{"postgres key=value outside", nil, cs("user=u host=evil.example port=5432"), false},
		{"postgres hosts list", nil, cs("postgres://u@good.example:5432,evil.example:5432/db"), false},
		{"mysql tcp()", nil, cs("u:p@tcp(good.example:3306)/db"), true},
		{"mysql tcp() outside", nil, cs("u:p@tcp(evil.example:3306)/db"), false},
		{"mysql bare", nil, cs("u:p@evil.example:3306/db"), false},
		{"mysql unix socket", nil, cs("u:p@unix(/var/run/mysqld.sock)/db"), false},
		{"mssql url", nil, cs("sqlserver://u:p@good.example:1433?database=db"), true},
		{"mssql url outside", nil, cs("sqlserver://u:p@evil.example:1433?database=db"), false},
		{"mssql ADO", nil, cs("server=evil.example,1433;user id=u;password=p"), false},
		{"mssql ADO granted", nil, cs("Server=tcp:good.example,1433;User Id=u"), true},
		{"oracle", nil, cs("oracle://u:p@evil.example:1521/svc"), false},
		{"mongodb+srv", nil, cs("mongodb+srv://u:p@good.example/db"), true},
		{"mongodb multi-host", nil, cs("mongodb://u:p@good.example:27017,evil.example:27017/db?replicaSet=r"), false},
		{"redis", nil, cs("redis://:p@evil.example:6379/0"), false},
		{"redis granted", nil, cs("rediss://:p@good.example:6380/0"), true},
		{"redis sentinel", nil, cs("redis-sentinel://:p@good.example:26379,evil.example:26379/mymaster"), false},
		{"unparseable string", nil, cs("%%%"), false},
		// URL values anywhere in the config
		{"url value", nil, map[string]interface{}{"url": "http://evil.example/feed?key=" + valVS}, false},
		{"url value granted", nil, map[string]interface{}{"url": "https://good.example/feed"}, true},
		{"nested url value", nil, map[string]interface{}{"opts": map[string]interface{}{"media_url": "https://evil.example/x"}}, false},
		{"url in a list", nil, map[string]interface{}{"urls": []interface{}{"https://good.example/a", "ftp://evil.example/b"}}, false},
		{"url inside text is not a destination", nil, map[string]interface{}{"message": "see https://evil.example/x"}, true},
	} {
		err := CheckEgress(ctx, "test.destprobe", c.keys, c.cfg)
		if c.allowed != (err == nil) {
			t.Errorf("%s: allowed=%v, got %v", c.name, c.allowed, err)
		}
	}
	if err := CheckEgress(context.Background(), "test.destprobe", sshLikeKeys, hostKeyCfg("evil.example")); err != nil {
		t.Errorf("a context under no agent access: %v", err)
	}
	if err := CheckEgress(ctx, "http.request", [][]string{{"url"}}, map[string]interface{}{"url": "https://evil.example"}); err != nil {
		t.Errorf("a node that checks at the send: %v", err)
	}
}

func hostKeyCfg(h string) map[string]interface{} { return map[string]interface{}{"host": h} }

// twoItemReader reads the secret and emits two items: one naming a granted
// host and one the host config["h2"] names (as http.request, a type a
// host-bound grant lets read).
type twoItemReader struct{}

func (twoItemReader) Type() string { return "http.request" }
func (twoItemReader) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	return []NodeOutput{{Handle: "main", Items: []Item{
		NewItem(map[string]interface{}{"v": config["v"], "h": "good.example"}),
		NewItem(map[string]interface{}{"v": config["v"], "h": config["h2"]}),
	}}}, nil
}

// toolRoot calls each of its ai_tool sub-nodes once and keeps the error.
type toolRoot struct{ errs *[]error }

func (toolRoot) Type() string { return "test.toolroot" }
func (r toolRoot) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	for _, s := range in.SubNodes {
		if s.Kind == "ai_tool" && s.Call != nil {
			_, err := s.Call(ctx, map[string]interface{}{"x": 1})
			*r.errs = append(*r.errs, err)
		}
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// Every path that resolves a node's config goes through resolveNodeConfig:
// each is run with a granted and an outside host. (Loop iterations are
// covered in internal/noderegistry with the real loop node; a resumed run, a
// parallel branch and a sub-workflow's run reach prepare or resolveFor again.)
func TestEveryResolutionPathHoldsTheGrant(t *testing.T) {
	type outcome struct {
		err error
		ran int
	}
	paths := map[string]func(t *testing.T, e *accessEnv, host string) outcome{
		"batch config (prepare)": func(t *testing.T, e *accessEnv, host string) outcome {
			wf, got := e.sendChain("wf-p-batch", sshLikeKeys, map[string]interface{}{"host": host})
			_, err := e.run(t, wf, nil)
			return outcome{err, len(*got)}
		},
		"each item (resolveFor), the second item's host": func(t *testing.T, e *accessEnv, host string) outcome {
			got := &[]map[string]interface{}{}
			e.reg.Register("http.request", func() NodeExecutor { return twoItemReader{} })
			e.reg.Register("test.destprobe", func() NodeExecutor { return destProbe{keys: sshLikeKeys, got: got} })
			wf := chain("wf-p-item",
				step("pi-r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs", "h2": host}),
				step("pi-d", "test.destprobe", "Sender", map[string]interface{}{"execute_once_per_item": true, "host": "{{ $json.h }}", "data": "{{ $json.v }}"}))
			_, err := e.run(t, wf, nil)
			ran := len(*got)
			if host != "good.example" { // the first item is always sent
				ran--
			}
			return outcome{err, ran}
		},
		"ai_tool call (callTool)": func(t *testing.T, e *accessEnv, host string) outcome {
			got := &[]map[string]interface{}{}
			var errs []error
			e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
			e.reg.Register("test.destprobe", func() NodeExecutor { return destProbe{keys: sshLikeKeys, got: got} })
			e.reg.Register("test.toolroot", func() NodeExecutor { return toolRoot{errs: &errs} })
			id := "wf-p-tool-" + strings.ReplaceAll(host, ".", "-")
			wf := chain(id,
				step(id+"r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}),
				step(id+"root", "test.toolroot", "Root", nil))
			wf.Nodes = append(wf.Nodes, WorkflowNode{ID: id + "m", WorkflowID: wf.ID, Type: "test.destprobe", Name: "Tool",
				Config: map[string]interface{}{"host": host, "data": "{{ $json.v }}"}})
			wf.Connections = append(wf.Connections, WorkflowConnection{ID: id + "mc", WorkflowID: wf.ID, SourceNodeID: id + "m", SourceHandle: "ai_tool", TargetNodeID: id + "root", TargetHandle: "ai_tool"})
			if _, err := e.run(t, wf, nil); err != nil || len(errs) != 1 {
				t.Fatalf("the run: %v (tool called %d times)", err, len(errs))
			}
			return outcome{errs[0], len(*got)}
		},
		"sub-node (resolveSubNodes)": func(t *testing.T, e *accessEnv, host string) outcome {
			got := &[]map[string]interface{}{}
			e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
			e.reg.Register("test.destprobe", func() NodeExecutor { return destProbe{keys: sshLikeKeys, got: got} })
			id := "wf-p-sub-" + strings.ReplaceAll(host, ".", "-")
			wf := chain(id,
				step(id+"r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}),
				step(id+"root", "test.echoprobe", "Root", nil))
			wf.Nodes = append(wf.Nodes, WorkflowNode{ID: id + "m", WorkflowID: wf.ID, Type: "test.destprobe", Name: "Model",
				Config: map[string]interface{}{"host": host, "data": "{{ $json.v }}"}})
			wf.Connections = append(wf.Connections, WorkflowConnection{ID: id + "mc", WorkflowID: wf.ID, SourceNodeID: id + "m", SourceHandle: "ai_languageModel", TargetNodeID: id + "root", TargetHandle: "ai_languageModel"})
			_, err := e.run(t, wf, nil)
			return outcome{err, 0} // a sub-node is not executed by the engine
		},
	}
	for name, run := range paths {
		out := run(t, newHostBound(t), "evil.example")
		if !errors.Is(out.err, secrets.ErrHostNotAllowed) {
			t.Errorf("%s: an outside host was not refused: %v", name, out.err)
		}
		if out.ran != 0 {
			t.Errorf("%s: Execute ran for the outside host", name)
		}
		if in := run(t, newHostBound(t), "good.example"); in.err != nil {
			t.Errorf("%s: a granted host: %v", name, in.err)
		}
	}
}

// Trigger sources resolve through ResolveTriggerCredentials: defence in depth:
// queue/DB consumers do not resolve vault secrets today, so in production this
// runs under no agent access and checks nothing.
func TestResolveTriggerCredentialsHoldsABrokerToTheGrantsHosts(t *testing.T) {
	egressClasses(t)
	SetEgressClassifier(func(typ string) (Egress, bool) { return EgressChecked, typ == "trigger.amqp" })
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	eng := newTestEngine(t, e.store, e.reg)
	wf := &Workflow{ID: "wf-trig", ProfileID: "default"}
	keys := [][]string{{"hostname"}, {"port"}}
	ctx := secrets.AllowResolving(e.ctx, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: wf.ID})
	if _, err := secrets.Resolve(ctx, e.db.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	cfg := func(host string) map[string]interface{} {
		return map[string]interface{}{"hostname": host, "port": 5672, "password": valVS}
	}
	if _, err := eng.ResolveTriggerCredentials(ctx, wf, "trigger.amqp", keys, cfg("evil.example")); !errors.Is(err, secrets.ErrHostNotAllowed) {
		t.Errorf("a broker outside the grant: %v", err)
	}
	if _, err := eng.ResolveTriggerCredentials(ctx, wf, "trigger.amqp", keys, cfg("good.example")); err != nil {
		t.Errorf("a granted broker: %v", err)
	}
	if _, err := eng.ResolveTriggerCredentials(e.ctx, wf, "trigger.amqp", keys, cfg("evil.example")); err != nil {
		t.Errorf("no agent access: %v", err)
	}
}

// The choke point: a config's @secret references are resolved in one place,
// followed by the egress check; a new path that resolves them elsewhere fails
// here until it goes through resolveNodeConfig.
func TestEverySecretResolutionGoesThroughResolveNodeConfig(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		secretCalls := strings.Count(src, "secrets.ResolveConfig(")
		wantCalls := 0
		if f == "execution_resolve.go" {
			wantCalls = 1
		}
		if secretCalls != wantCalls {
			t.Errorf("%s resolves @secret references %d times outside resolveNodeConfig (want %d)", f, secretCalls, wantCalls)
		}
		if n := strings.Count(src, "resolveNodeConfig("); (f == "execution_node.go" && n != 1) || (f == "execution_peritem.go" && n != 1) || (f == "execution_subnodes.go" && n != 2) {
			t.Errorf("%s calls resolveNodeConfig %d times", f, n)
		}
	}
}
