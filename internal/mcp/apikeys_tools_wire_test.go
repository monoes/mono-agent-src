package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/orggrant"
)

// wireSession is one Serve over a pipe, driven in order: each call is sent after the
// answer to the one before arrived, so a sequence can depend on its own earlier
// calls, and everything the server wrote to its stdout is kept. (serveLines hands
// every line to a goroutine of its own, and closes the database when its input
// ends.)
type wireSession struct {
	t    *testing.T
	pw   *io.PipeWriter
	out  *lockedBuffer
	done chan error
	next int
}

func newWireSession(t *testing.T, s *Server) *wireSession {
	t.Helper()
	pr, pw := io.Pipe()
	w := &wireSession{t: t, pw: pw, out: &lockedBuffer{}, done: make(chan error, 1), next: 1}
	go func() { w.done <- s.Serve(context.Background(), pr, w.out) }()
	t.Cleanup(func() { pw.Close() })
	return w
}

// send writes a tools/call and returns its id, without waiting for the answer: a
// host may have several calls in flight.
func (w *wireSession) send(name string, args map[string]any) string {
	w.t.Helper()
	id := strconv.Itoa(w.next)
	if _, err := fmt.Fprintln(w.pw, callToolReq(w.next, name, args)); err != nil {
		w.t.Fatal(err)
	}
	w.next++
	return id
}

// call sends a tools/call and returns the text of its answer and whether it is an error.
func (w *wireSession) call(name string, args map[string]any) (string, bool) {
	w.t.Helper()
	return w.await(w.send(name, args))
}

// await waits for the answer to a request sent earlier.
func (w *wireSession) await(id string) (string, bool) {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !w.out.responseIDsSeen()[id] {
		if time.Now().After(deadline) {
			w.t.Fatalf("no answer to request %s", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var resps []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(w.out.String()), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			w.t.Fatalf("a line of stdout that is not JSON-RPC: %q", scrubbed(line))
		}
		resps = append(resps, m)
	}
	return toolText(w.t, respByID(w.t, resps, id))
}

// finish ends the input and returns everything the server wrote.
func (w *wireSession) finish() string {
	w.t.Helper()
	w.pw.Close()
	select {
	case err := <-w.done:
		if err != nil {
			w.t.Fatalf("Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		w.t.Fatal("Serve did not return after its input ended")
	}
	return w.out.String()
}

// No key is on the wire: the key tools change none, and the key the user made is
// in no list or error, including the answers to calls that go wrong, and to a
// caller that pastes the key where an id or a name goes.
func TestNoKeyIsOnTheWire(t *testing.T) {
	s, _ := newAPIKeyServer(t, true)
	id, secret := createKey(t, s, "wire", true)
	w := newWireSession(t, s)

	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"api_key_create", map[string]any{"name": "made", "context": true}},
		{"api_key_update", map[string]any{"id": id, "name": "wire-2", "context": false}},
		{"api_key_revoke", map[string]any{"id": id}},
		{"api_key_update", map[string]any{"id": secret, "name": "x"}}, // the key pasted where an id goes
		{"api_key_revoke", map[string]any{"id": secret}},              // the same
		{"api_key_update", map[string]any{"id": id, "name": secret}},  // the key pasted where a name goes
		{"api_key_create", map[string]any{"name": secret}},            // the same, for a new key
	} {
		if text, isErr := w.call(c.name, c.args); !isErr {
			t.Errorf("%s changed a key: %s", c.name, scrubbed(text))
		}
	}
	w.call("api_key_list", nil)
	w.call("api_key_list", map[string]any{"include_revoked": true})
	w.call("api_key_list", map[string]any{"include_revoked": "yes"}) // a malformed call
	out := w.finish()

	if keyRE.MatchString(out) {
		t.Fatal("a key is on the wire")
	}
	// Nor is its random part or its hash.
	if strings.Contains(out, secret[len(apikeys.KeyPrefix):]) {
		t.Error("the random part of the key is on the wire")
	}
	if strings.Contains(out, apikeys.HashKey(secret)) {
		t.Error("the key's hash is on the wire")
	}
}

// Without the flag a create is refused, and no key is on the wire at all.
func TestARefusedCreateLeavesNoKeyOnTheWire(t *testing.T) {
	s, dbPath := newAPIKeyServer(t, false)
	w := newWireSession(t, s)

	text, isErr := w.call("api_key_create", map[string]any{"name": "nope"})
	if !isErr || !strings.Contains(text, "--allow-mutations") {
		t.Errorf("create without the flag: %q (error %v)", scrubbed(text), isErr)
	}
	w.call("api_key_list", map[string]any{"include_revoked": true})
	out := w.finish()

	if keyRE.MatchString(out) {
		t.Error("a key is on the wire")
	}
	if n := keyRows(t, dbPath); n != 0 {
		t.Errorf("a refused create stored %d keys", n)
	}
}

// An org role's tool provider runs `mcp --grant`: it must not be able to list, mint
// or revoke API keys, which would hand the role access to every runtime of the machine.
func TestGrantModeServesNoAPIKeyTool(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	liveHeartbeat(t)

	// The requests go through the JSON-RPC layer, the way a role's tool provider is
	// called, and in one Serve: it closes the database when its input ends.
	calls := []struct {
		name string
		args map[string]any
	}{
		{"api_key_list", map[string]any{}},
		{"api_models_list", map[string]any{}},
		{"api_key_create", map[string]any{"name": "role-made"}},
		{"api_key_update", map[string]any{"id": "key_zzzzzzzzzzzz", "context": true}},
		{"api_key_revoke", map[string]any{"id": "key_zzzzzzzzzzzz"}},
		// The tools of the API's server: its status, its settings, the restart and the auto model.
		{"api_status", map[string]any{}},
		{"api_config_get", map[string]any{}},
		{"api_config_set", map[string]any{"set": map[string]any{"max_concurrent": "8"}}},
		{"api_config_apply", map[string]any{}},
		{"api_auto_set", map[string]any{"enabled": true, "acknowledge_egress": true}},
	}
	lines := []string{request(100, "tools/list", map[string]interface{}{})}
	for i, c := range calls {
		lines = append(lines, callToolReq(i+1, c.name, c.args))
	}
	resps := serveLines(t, f.server, lines...)

	for i, c := range calls {
		text, isErr := toolText(t, respByID(t, resps, strconv.Itoa(i+1)))
		if !isErr || !strings.HasPrefix(text, codeRefusedGrant) {
			t.Errorf("%s in grant mode: %q (error %v), want a refusal %s", c.name, text, isErr, codeRefusedGrant)
		}
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(respByID(t, resps, "100")["result"], &res); err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if strings.HasPrefix(tl.Name, "api_") {
			t.Errorf("grant mode lists %s", tl.Name)
		}
	}
	var n int
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a grant-mode call stored %d keys (%v)", n, err)
	}
	// Nor did a call change the API's settings or switch a Jev surface on.
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'api_gateway_config' OR key LIKE 'jev.%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a grant-mode call saved %d settings of the API or of Jev (%v)", n, err)
	}
}
