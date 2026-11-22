### Task 1: Who the task tools act as (spec D18)

**Files:**
- Create: `internal/mcp/task_actor.go`
- Modify: `internal/mcp/server.go` (the `Server` struct, `NewServer`, the `initialize` case of `handleLine`)
- Test: `internal/mcp/task_actor_test.go`

**Interfaces:**
- Consumes: `tasks.Actor`, `tasks.Agent`, `tasks.NewStore`, `Store.Add`, `Store.Claim` (P1); the test helpers `serveLines`, `request`, `sideDB` (existing in `internal/mcp`).
- Produces (later tasks use these exactly): `maxClientLabel = 53`; `var newActorSuffix func() string`; `clientLabel(name string) string`; `(*Server).recordClient(params json.RawMessage)`; `(*Server).taskActor() tasks.Actor`; the `Server` fields `clientMu sync.Mutex`, `clientName`, `actorName`, `actorSuffix string` (tests set `actorSuffix` directly before the first task call); the test helpers `withSuffixes(t, suffixes ...string)` and `initializeAs(t, s, params map[string]any)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/mcp/task_actor_test.go`:

```go
package mcp

// The task tools act as an agent named after the client (spec D18): agent:<client>#<four hex
// digits>. The name comes from initialize, the model cannot choose it, and every session has a
// suffix of its own, so two sessions of one client are two claimants.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

func TestClientLabel(t *testing.T) {
	long := strings.Repeat("x", 300)
	for in, want := range map[string]string{
		"claude-code":          "claude-code",
		"Claude Desktop":       "Claude-Desktop",
		"cursor (vscode)":      "cursor-vscode",
		"a#b:c":                "a-b-c",
		"me@host.local":        "me@host.local",
		"--x--":                "x",
		"":                     "mcp",
		"   ":                  "mcp",
		"\U000065e5\U0000672c": "mcp",
		"\U0000202eevil":       "evil",
		long:                   long[:maxClientLabel],
	} {
		if got := clientLabel(in); got != want {
			t.Errorf("clientLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// withSuffixes makes the servers a test builds take these suffixes, in turn.
func withSuffixes(t *testing.T, suffixes ...string) {
	t.Helper()
	was := newActorSuffix
	n := 0
	newActorSuffix = func() string {
		s := suffixes[n%len(suffixes)]
		n++
		return s
	}
	t.Cleanup(func() { newActorSuffix = was })
}

// initializeAs sends one initialize with these params, as a host does first.
func initializeAs(t *testing.T, s *Server, params map[string]any) {
	t.Helper()
	if resps := serveLines(t, s, request(1, "initialize", params)); len(resps) != 1 || resps[0]["result"] == nil {
		t.Fatalf("initialize: %v", resps)
	}
}

func TestTheTaskActorIsNamedAfterTheClientInInitialize(t *testing.T) {
	withSuffixes(t, "beef")
	s := NewServer(Options{Version: "test"})
	initializeAs(t, s, map[string]any{"protocolVersion": "2024-11-05", "clientInfo": map[string]any{"name": "Claude Code", "version": "2.1"}})
	initializeAs(t, s, map[string]any{"clientInfo": map[string]any{"name": "someone-else"}})
	if a := s.taskActor(); a.Kind != tasks.Agent || a.Name != "agent:Claude-Code#beef" {
		t.Errorf("actor = %+v, want the agent agent:Claude-Code#beef (the first client named)", a)
	}

	// Before any initialize the client is "mcp", and the first task call fixes the name: a later
	// initialize does not rename whoever holds a claim.
	early := NewServer(Options{Version: "test"})
	if got := early.taskActor().Name; got != "agent:mcp#beef" {
		t.Errorf("before initialize: %q", got)
	}
	initializeAs(t, early, map[string]any{"clientInfo": map[string]any{"name": "claude-code"}})
	if got := early.taskActor().Name; got != "agent:mcp#beef" {
		t.Errorf("a later initialize renamed the claimant: %q", got)
	}

	// An initialize without a clientInfo, as the older tests send, names no client.
	bare := NewServer(Options{Version: "test"})
	initializeAs(t, bare, map[string]any{})
	if got := bare.taskActor().Name; got != "agent:mcp#beef" {
		t.Errorf("an initialize without a client: %q", got)
	}
}

func TestTwoSessionsOfOneClientAreTwoClaimants(t *testing.T) {
	real := newActorSuffix()
	if !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(real) {
		t.Errorf("a server's suffix is %q, want four hex digits", real)
	}
	withSuffixes(t, "0001", "0002")
	a, b := NewServer(Options{Version: "test"}), NewServer(Options{Version: "test"})
	for _, s := range []*Server{a, b} {
		initializeAs(t, s, map[string]any{"clientInfo": map[string]any{"name": "claude-code"}})
	}
	if an, bn := a.taskActor().Name, b.taskActor().Name; an != "agent:claude-code#0001" || bn != "agent:claude-code#0002" {
		t.Errorf("two sessions of one client: %q and %q, want a suffix each", an, bn)
	}
}

// Whatever a client calls itself, the name is one the store takes: a claim under it succeeds.
// The store, not a copy of its rule, is the judge; that holds for the reserved labels (you, agent,
// capture, chrome, os) in any case too, since an actor name always holds "agent:" and "#".
func TestEveryClientNameMakesAClaimableActor(t *testing.T) {
	db := sideDB(t, testdb.Path(t))
	store := tasks.NewStore(db.DB)
	ctx := context.Background()
	for i, client := range []string{"Claude Desktop", "a#b:c", "", strings.Repeat("Z", 300), "\U0000202eevil", "\U000065e5\U0000672c",
		"me@host.local", "you", "agent", "capture", "chrome", "os", "YOU", "Agent"} {
		s := NewServer(Options{Version: "test"})
		params, err := json.Marshal(map[string]any{"clientInfo": map[string]any{"name": client}})
		if err != nil {
			t.Fatal(err)
		}
		s.recordClient(params)
		task, _, err := store.Add(ctx, "default", tasks.AddInput{Title: fmt.Sprintf("job %d", i), Ready: true}, tasks.Actor{Kind: tasks.Human})
		if err != nil {
			t.Fatal(err)
		}
		actor := s.taskActor()
		if _, err := store.Claim(ctx, "default", task.ID, actor, 0); err != nil {
			t.Errorf("client %q: the store refuses its actor %q: %v", client, actor.Name, err)
		}
	}
}
```

Then run `grep -nP '[^\x00-\x7F]' internal/mcp/task_actor_test.go`: it must print nothing (the escapes stay escapes).

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessionsOfOneClient|TestEveryClientName' -count=1`
Expected: FAIL to compile (`undefined: maxClientLabel`, `undefined: newActorSuffix`, `s.taskActor undefined`).

- [ ] **Step 3: Write `internal/mcp/task_actor.go`**

```go
package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// maxClientLabel is the room an actor name leaves for the client's name: the
// task store takes names of 64 characters, and "agent:", "#" and four hex
// digits take 11 of them.
const maxClientLabel = 53

// newActorSuffix makes the four hex digits that tell two sessions of one
// client apart (spec D18). A variable, so a test can give servers known ones.
var newActorSuffix = func() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// clientLabel is a client's name as it may stand in an actor name: ASCII
// letters, digits, '.', '_' and '@' as they are, and every other run of
// characters one '-' ('#' and ':' are the separators of the name), at most
// maxClientLabel long; "mcp" when nothing is left.
func clientLabel(name string) string {
	var b strings.Builder
	gap := false
	for _, r := range name {
		if b.Len() > maxClientLabel {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '@' {
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			gap = false
			b.WriteRune(r)
			continue
		}
		gap = true
	}
	label := b.String()
	if len(label) > maxClientLabel {
		label = label[:maxClientLabel]
	}
	if label = strings.TrimRight(label, "-"); label == "" {
		return "mcp"
	}
	return label
}

// recordClient keeps the name a client gives in initialize's clientInfo; the
// first initialize that names a client wins. Params without one, or that do not
// parse, change nothing.
func (s *Server) recordClient(params json.RawMessage) {
	var p struct {
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || strings.TrimSpace(p.ClientInfo.Name) == "" {
		return
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.clientName == "" {
		s.clientName = p.ClientInfo.Name
	}
}

// taskActor is who the task tools act as (spec D18): an agent named
// agent:<client>#<four hex digits>, after the client that sent initialize and a
// suffix of this server's own, so two sessions of one client are two claimants.
// The model does not choose it. The first task call fixes it, so a later
// initialize cannot rename the holder of a claim.
func (s *Server) taskActor() tasks.Actor {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.actorName == "" {
		s.actorName = "agent:" + clientLabel(s.clientName) + "#" + s.actorSuffix
	}
	return tasks.Actor{Kind: tasks.Agent, Name: s.actorName}
}
```

- [ ] **Step 4: Edit `internal/mcp/server.go`**

Three Edit calls.

1. The end of the `Server` struct. Replace

```go
	rtMu sync.Mutex
}
```

with

```go
	rtMu sync.Mutex

	// clientMu guards who this server serves (task_actor.go): the client's
	// name from initialize, the actor name the task tools fix from it, and
	// this server's own suffix. Requests run on goroutines of their own.
	clientMu    sync.Mutex
	clientName  string
	actorName   string
	actorSuffix string
}
```

2. In `NewServer`, replace `	return &Server{opts: opts}` with `	return &Server{opts: opts, actorSuffix: newActorSuffix()}`.

3. In `handleLine`, replace

```go
	case "initialize":
		return s.result(req.ID, map[string]interface{}{
```

with

```go
	case "initialize":
		s.recordClient(req.Params)
		return s.result(req.ID, map[string]interface{}{
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `gofmt -l internal/mcp` then `go vet ./internal/mcp/` then `go test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessionsOfOneClient|TestEveryClientName|TestServer' -count=1`
Expected: nothing from `gofmt` (if it lists `task_actor_test.go`, run `gofmt -w` on it: the map literal's alignment), vet clean, PASS (the existing `TestServer*` tests too: `initialize` answers as before).

- [ ] **Step 6: Commit**

```
git add internal/mcp/task_actor.go internal/mcp/task_actor_test.go internal/mcp/server.go
```
then
```
git commit -m "feat(tasks): the MCP server names its caller after the client, agent:<client>#<4 hex>" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

