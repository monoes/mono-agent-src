package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

// destProbe is a node type with destination keys (as ssh, a queue or LDAP
// have) that records what Execute was given. The tests below name hosts in
// it; 6i: a grant that names hosts holds every node type with destination
// keys to them, not only the http nodes.
type destProbe struct {
	keys [][]string
	got  *[]map[string]interface{}
}

func (destProbe) Type() string { return "test.destprobe" }
func (p destProbe) CredentialDestinationKeys() [][]string {
	return p.keys
}
func (p destProbe) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	*p.got = append(*p.got, config)
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(config)}}}, nil
}

// sendChain is the route the check closes: a step that may read the secret
// (http.request, which checks its destination at the send) puts it in the
// run's data, and a step with destination keys that has no such check gets
// it from there and names a host of its own.
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

var sshLikeKeys = [][]string{{"host"}, {"port"}}

func TestHostBoundGrantHoldsANodeWithDestinationKeysToTheGrantsHosts(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	wf, got := e.sendChain("wf-out", sshLikeKeys, map[string]interface{}{"host": "evil.example", "port": 22})
	execID, err := e.run(t, wf, nil)
	if !errors.Is(err, secrets.ErrHostNotAllowed) && !strings.Contains(e.stored(t, execID), secrets.ErrHostNotAllowed.Error()) {
		t.Errorf("a host outside the grant was not refused: err=%v", err)
	}
	if len(*got) != 0 {
		t.Errorf("Execute ran with the secret in its config: %v", *got)
	}
	if strings.Contains(e.stored(t, execID), valVS) {
		t.Error("the value reached a stored record")
	}
}

func TestHostBoundGrantAllowsAndAuditsAHostInsideTheList(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	wf, got := e.sendChain("wf-in", sshLikeKeys, map[string]interface{}{"host": "GOOD.example", "port": 22})
	if _, err := e.run(t, wf, nil); err != nil {
		t.Fatalf("a granted host: %v", err)
	}
	if len(*got) != 1 || (*got)[0]["data"] != valVS {
		t.Errorf("Execute: %v", *got)
	}
	rows, err := secrets.AgentAccessAudit(e.ctx, e.db.DB, "default")
	if err != nil || len(rows) != 1 || rows[0].Secret != "vs" {
		t.Errorf("audit: %v %v", rows, err)
	}
}

func TestSendHostCheckLeavesAnyHostGrantsAndApprovedWorkflowsAlone(t *testing.T) {
	// An --any-host grant (what a grant made before host binding reads as:
	// migration 111 gave those rows any_host = 1) has no binding by design.
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	wf, got := e.sendChain("wf-any", sshLikeKeys, map[string]interface{}{"host": "evil.example"})
	if _, err := e.run(t, wf, nil); err != nil || len(*got) != 1 {
		t.Errorf("no host binding, so the send is allowed: %v %v", err, *got)
	}
	// An approved workflow is not under agent access: no check, as before.
	e = newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	wf, got = e.sendChain("wf-ok", sshLikeKeys, map[string]interface{}{"host": "evil.example"})
	createSQLWorkflow(t, e.store, wf)
	if err := e.store.ApproveDefinition(e.ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.store.GetWorkflow(e.ctx, wf.ID)
	if _, err := e.runStored(t, cur); err != nil || len(*got) != 1 {
		t.Errorf("an approved workflow: %v %v", err, *got)
	}
}

// A run that read no secret is not held, though its nodes name hosts.
func TestSendHostCheckHoldsOnlyARunThatReadASecret(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	got := &[]map[string]interface{}{}
	e.reg.Register("test.destprobe", func() NodeExecutor { return destProbe{keys: sshLikeKeys, got: got} })
	wf := chain("wf-none", step("s", "test.destprobe", "Sender", map[string]interface{}{"host": "evil.example"}))
	if _, err := e.run(t, wf, nil); err != nil || len(*got) != 1 {
		t.Errorf("a run with no secret: %v %v", err, *got)
	}
}

func TestCheckSendDestinationsKeyShapes(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	ctx := secrets.AllowResolving(e.ctx, e.db.DB, "default", secrets.AccessOrigin{WorkflowID: "w", ExecutionID: "x"})
	ctx = secrets.WithAccessNode(ctx, "n", "N")
	if _, err := secrets.Resolve(ctx, e.db.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		keys   [][]string
		cfg    map[string]interface{}
		refuse bool
	}{
		{"bare host", [][]string{{"host"}}, map[string]interface{}{"host": "evil.example"}, true},
		{"host:port", [][]string{{"host"}}, map[string]interface{}{"host": "evil.example:2222"}, true},
		{"url", [][]string{{"base_url"}}, map[string]interface{}{"base_url": "https://evil.example/api"}, true},
		{"second alias of a group", [][]string{{"url", "base_url"}}, map[string]interface{}{"base_url": "https://evil.example"}, true},
		{"list of brokers", [][]string{{"brokers"}}, map[string]interface{}{"brokers": "good.example:9092,evil.example:9092"}, true},
		{"nested block", [][]string{{"embedding.base_url"}}, map[string]interface{}{"embedding": map[string]interface{}{"base_url": "https://evil.example"}}, true},
		{"host still an expression", [][]string{{"host"}}, map[string]interface{}{"host": "{{ $json.h }}"}, true},
		{"granted host", [][]string{{"host"}}, map[string]interface{}{"host": "good.example:22"}, false},
		{"granted url", [][]string{{"base_url"}}, map[string]interface{}{"base_url": "https://good.example/x"}, false},
		{"port alone", [][]string{{"port"}}, map[string]interface{}{"port": 5432}, false},
		{"vendor name", [][]string{{"credential_platform"}}, map[string]interface{}{"credential_platform": "openai"}, false},
		{"unset key", [][]string{{"host"}}, map[string]interface{}{}, false},
		{"a node that checks at the send", [][]string{{"url"}}, map[string]interface{}{"url": "https://evil.example"}, false},
	} {
		typ := "test.destprobe"
		if c.name == "a node that checks at the send" {
			typ = "http.request"
		}
		err := CheckSendDestinations(ctx, typ, c.keys, c.cfg)
		if c.refuse != errors.Is(err, secrets.ErrHostNotAllowed) {
			t.Errorf("%s: refuse=%v, got %v", c.name, c.refuse, err)
		}
	}
	if err := CheckSendDestinations(context.Background(), "test.destprobe", sshLikeKeys, map[string]interface{}{"host": "evil.example"}); err != nil {
		t.Errorf("a context under no agent access: %v", err)
	}
}

// Trigger sources (queue and database consumers) resolve through
// ResolveTriggerCredentials: the same hold on the broker they dial.
func TestResolveTriggerCredentialsHoldsABrokerToTheGrantsHosts(t *testing.T) {
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

// A sub-node (a model, an embedding) is held to the grant's hosts too: its
// root sends to the host its config names.
func TestHostBoundGrantHoldsASubNodeWithDestinationKeys(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Hosts: []string{"good.example"}})
	got := &[]map[string]interface{}{}
	e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
	e.reg.Register("test.destprobe", func() NodeExecutor { return destProbe{keys: sshLikeKeys, got: got} })
	for host, wantErr := range map[string]bool{"evil.example": true, "good.example": false} {
		id := "wf-sub-" + strings.ReplaceAll(host, ".", "-")
		wf := chain(id,
			step(id+"r", "http.request", "Reader", map[string]interface{}{"v": "@secret:vs"}),
			step(id+"root", "test.echoprobe", "Root", nil))
		wf.Nodes = append(wf.Nodes, WorkflowNode{ID: id + "m", WorkflowID: wf.ID, Type: "test.destprobe", Name: "Model",
			Config: map[string]interface{}{"host": host, "data": "{{ $json.v }}"}})
		wf.Connections = append(wf.Connections, WorkflowConnection{ID: id + "mc", WorkflowID: wf.ID, SourceNodeID: id + "m", SourceHandle: "ai_languageModel", TargetNodeID: id + "root", TargetHandle: "ai_languageModel"})
		_, err := e.run(t, wf, nil)
		if wantErr != errors.Is(err, secrets.ErrHostNotAllowed) {
			t.Errorf("sub-node at %s: %v", host, err)
		}
	}
}
