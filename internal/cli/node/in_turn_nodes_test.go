package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

const (
	pkgDB    = "github.com/monoes/mono-agent/internal/nodes/db"
	pkgMCP   = "github.com/monoes/mono-agent/internal/nodes/mcp"
	pkgHTTP  = "github.com/monoes/mono-agent/internal/nodes/http"
	pkgVault = "github.com/monoes/mono-agent/internal/nodes/vault"
)

// rawRecorder is a TCP listener that keeps every byte it is sent and then closes the connection: a database,
// queue or MCP node that dials it hands it whatever it would have handed a server.
func rawRecorder(t *testing.T, pg bool) (port int, got func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var buf strings.Builder
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				record := func(b []byte) {
					mu.Lock()
					buf.Write(b)
					mu.Unlock()
				}
				if pg {
					fakePostgres(c, record)
					return
				}
				b, _ := io.ReadAll(io.LimitReader(c, 1<<16))
				record(b)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port, func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// fakePostgres answers the PostgreSQL wire protocol just far enough for a ping to succeed (trust
// authentication, ready for query, every statement an empty query) and records every byte it is sent.
func fakePostgres(c net.Conn, record func([]byte)) {
	readN := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(c, b); err != nil {
			return nil
		}
		record(b)
		return b
	}
	be32 := func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
	for {
		head := readN(4)
		if head == nil {
			return
		}
		body := readN(be32(head) - 4)
		if body == nil {
			return
		}
		if len(body) >= 4 && be32(body) == 80877103 { // SSLRequest
			_, _ = c.Write([]byte("N"))
			continue
		}
		break
	}
	_, _ = c.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 0})
	_, _ = c.Write([]byte{'Z', 0, 0, 0, 5, 'I'})
	status := byte('I')
	for {
		t := readN(1)
		if t == nil {
			return
		}
		l := readN(4)
		if l == nil {
			return
		}
		var q []byte
		if be32(l) > 4 {
			if q = readN(be32(l) - 4); q == nil {
				return
			}
		}
		switch t[0] {
		case 'X':
			return
		case 'Q':
			tag := "SELECT 0"
			for _, w := range []string{"BEGIN", "COMMIT", "ROLLBACK"} {
				if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(string(q))), w) {
					tag = w
				}
			}
			if tag == "BEGIN" {
				status = 'T'
			} else if tag == "COMMIT" || tag == "ROLLBACK" {
				status = 'I'
			}
			_, _ = c.Write(append([]byte{'C', 0, 0, 0, byte(4 + len(tag) + 1)}, append([]byte(tag), 0)...))
			_, _ = c.Write([]byte{'Z', 0, 0, 0, 5, status})
		case 'S':
			_, _ = c.Write([]byte{'Z', 0, 0, 0, 5, status})
		}
	}
}

// secretReaderConfig is the config that puts an @secret reference in every field a node of this kind reads
// it from, aimed at the recorder. "" for a type with no config here.
func secretReaderConfig(nodeType, pkg string, port int) map[string]interface{} {
	ref := "@secret:vs"
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	switch {
	case pkg == pkgDB:
		hostPort := fmt.Sprintf("127.0.0.1:%d", port)
		c := map[string]interface{}{"host": "127.0.0.1", "hostname": "127.0.0.1", "port": port, "url": url, "timeout": 2, "connect_timeout": 2,
			"brokers": hostPort, "account": "x", "database": "d", "user": "u", "username": "u",
			"connection_string": "postgres://u:p@" + hostPort + "/d?sslmode=disable&connect_timeout=2"}
		switch nodeType {
		case "db.mysql":
			c["connection_string"] = "u:p@tcp(" + hostPort + ")/d?timeout=2s"
		case "db.mssql":
			c["connection_string"] = "sqlserver://u:p@" + hostPort + "?database=d&connection+timeout=2"
		case "db.oracle":
			c["connection_string"] = "oracle://u:p@" + hostPort + "/svc"
		case "db.mongodb":
			c["connection_string"] = "mongodb://" + hostPort + "/?serverSelectionTimeoutMS=1500&connectTimeoutMS=1500"
		case "queue.rabbitmq", "queue.amqp":
			c["connection_string"] = "amqp://" + hostPort
		case "db.ldap":
			c["operation"] = "search"
		case "db.redis":
			c["operation"] = "get"
			c["connection_string"] = "redis://" + hostPort + "?max_retries=-1&dial_timeout=1s&read_timeout=1s&write_timeout=1s"
		}
		for _, k := range []string{"query", "params", "data", "where", "query_replacement", "values", "value_to_match_on", "conditions", "bind_params", "values_to_send",
			"dn", "target_dn", "attribute_id", "value", "attributes", "base_dn", "search_text", "custom_filter", "search_for",
			"message", "message_key", "headers", "topic", "queue", "routing_key", "sink", "header_parameters", "send_only_property",
			"fields", "update_key", "index_name", "index_definition", "collection", "filter", "update", "document", "pipeline",
			"key", "field", "property_name", "key_pattern", "list", "channel", "message_data", "ttl", "ttl_seconds"} {
			c[k] = ref
		}
		return c
	case nodeType == "mcp.call":
		return map[string]interface{}{"transport": "http", "url": url, "operation": "list_tools", "timeout": 2,
			"headers": map[string]interface{}{"Authorization": "Bearer " + ref}}
	case nodeType == "http.request":
		return map[string]interface{}{"url": url, "method": "POST", "body": ref, "headers": map[string]interface{}{"Authorization": ref}}
	case nodeType == "vault.secret_get":
		return map[string]interface{}{"name": "vs"}
	}
	return nil
}

// registryTypes classifies the types node run can run, from the registry it builds from.
type registryType struct {
	name string
	pkg  string
	dest [][]string // CredentialDestinationKeys, nil when the executor has none
}

func registryTypes(t *testing.T) []registryType {
	t.Helper()
	reg := noderegistry.Build(nil)
	var out []registryType
	for _, name := range reg.Types() {
		f, ok := reg.Get(name)
		if !ok {
			t.Fatalf("registry lists %s but does not have it", name)
		}
		ex := f()
		rt := registryType{name: name}
		if v := reflect.TypeOf(ex); v != nil {
			for v.Kind() == reflect.Ptr {
				v = v.Elem()
			}
			rt.pkg = v.PkgPath()
		}
		if d, ok := ex.(workflow.CredentialDestinations); ok {
			rt.dest = d.CredentialDestinationKeys()
		}
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// runRaw runs `node run nodeType` as caller c against a recorder, under grant (nil: agent access off), and returns
// the run with everything the recorder was sent.
func runRaw(t *testing.T, c callerContext, nodeType string, mk func(port int) map[string]interface{}, grant *secrets.AccessGrant, extra ...string) (secretRun, string) {
	t.Helper()
	dbPath := nodeVaultTestDB(t)
	c.setup(t)
	seedVaultSecret(t, dbPath, "vs", inTurnSecret)
	port, wire := rawRecorder(t, strings.Contains(nodeType, "postgres") || strings.HasSuffix(nodeType, "timescaledb") || strings.HasSuffix(nodeType, "cratedb") || strings.HasSuffix(nodeType, "questdb"))
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
	raw, _ := json.Marshal(map[string]interface{}{"config": mk(port)})
	out, err := runNodeSubcmdStdin(t, d, string(raw), append([]string{nodeType}, extra...)...)
	db, derr := storage.NewDatabase(dbPath)
	if derr != nil {
		t.Fatal(derr)
	}
	defer db.Close()
	rows, aerr := secrets.AgentAccessAudit(context.Background(), db.DB, "default")
	if aerr != nil {
		t.Fatal(aerr)
	}
	return secretRun{out: out, err: err, audit: len(rows)}, wire()
}

// ---------------------------------------------------------------------------------------------------------
// The generic table. Which node types read a secret is derived from the registry node run builds from: a type
// whose executor implements workflow.CredentialDestinations gets the destination case; a type of a package
// whose nodes resolve @secret references (db, mcp, http.request, vault.secret_get) gets the reference case;
// everything else is either listed below with the reason it cannot run here, or reads no secret, which the
// source pin (TestEverySourceFileThatReadsASecretHasAnInTurnCase) holds to the known files.
// ---------------------------------------------------------------------------------------------------------

// noReferenceCase: registry types that read @secret references and cannot be run to the point of resolving one
// here, with the reason. A stale entry (a type that is gone, or one the table now reaches) fails.
var noReferenceCase = map[string]string{
	"db.mysql":       "resolves its per-item fields only after a successful ping; needs a live MySQL server (db.postgres covers the shared resolveForItem path)",
	"db.mssql":       "resolves its per-item fields only after a successful ping; needs a live SQL Server (db.postgres covers the shared resolveForItem path)",
	"db.oracle":      "resolves its per-item fields only after a successful ping; needs a live Oracle server (db.postgres covers the shared resolveForItem path)",
	"db.snowflake":   "talks HTTPS to <account>.snowflakecomputing.com; there is no loopback seam",
	"queue.kafka":    "resolves its per-item fields only after connecting to a live broker",
	"queue.amqp":     "resolves its per-item fields only after connecting to a live broker",
	"queue.rabbitmq": "resolves its per-item fields only after connecting to a live broker",
	"queue.mqtt":     "resolves its per-item fields only after connecting to a live broker",
}

// browserPackages: executors of these packages read {{secret:}} in an action through the browser bridge.
var browserPackages = map[string]string{
	"github.com/monoes/mono-agent/internal/nodes":            "browser nodes: need the browser bridge; a non-builtin package is refused before it (TestNodeRunInATurnRunsNoRecordedBrowserAction)",
	"github.com/monoes/mono-agent/internal/nodes/browserjev": "goal-driven browser node: needs the user's browser",
}

// looseText: types whose refusal text legitimately differs between a marked agent (refused by the CLI's marker
// check) and a process in a turn (refused by the node); the value must still be in no output.
var looseText = map[string]bool{"vault.secret_get": true}

// noGrantRun: types the run with the profile's agent access on is not compared for, with the reason.
var noGrantRun = map[string]string{"vault.secret_get": "a reveal needs the profile's Reveal level, which 6b puts on the same verdict"}

func shortErr(r secretRun) string {
	if r.err == nil {
		return "<nil>"
	}
	e := r.err.Error()
	if len(e) > 160 {
		e = e[:160]
	}
	return e
}

func refusedText(r secretRun) bool { return strings.Contains(r.shown(), "is not read from the vault") }

var anyHost = &secrets.AccessGrant{AnyHost: true}

func TestNodeRunInATurnReferenceCasesForEveryNodeThatReadsSecrets(t *testing.T) {
	types := registryTypes(t)
	have := map[string]bool{}
	var cases []registryType
	for _, rt := range types {
		have[rt.name] = true
		if secretReaderConfig(rt.name, rt.pkg, 1) != nil {
			if _, listed := noReferenceCase[rt.name]; !listed {
				cases = append(cases, rt)
			}
		}
	}
	for name := range noReferenceCase {
		if !have[name] {
			t.Errorf("noReferenceCase lists %s, which is not a registered node type any more: remove it", name)
		}
	}
	// a type of a db-package that is not in the table nor listed is a new node nobody gave a case
	for _, rt := range types {
		if rt.pkg == pkgDB {
			if _, listed := noReferenceCase[rt.name]; !listed && secretReaderConfig(rt.name, rt.pkg, 1) == nil {
				t.Errorf("%s is a db-package node with neither an in-turn case nor an entry in noReferenceCase", rt.name)
			}
		}
	}
	inTurn, marked := callerContexts()[1], callerContexts()[5]
	for _, rt := range cases {
		mk := func(p int) map[string]interface{} { return secretReaderConfig(rt.name, rt.pkg, p) }
		t.Run(rt.name, func(t *testing.T) {
			// the marked agent under a grant must reach a resolution, or this case proves nothing
			gm, gmWire := runRaw(t, marked, rt.name, mk, anyHost)
			if _, skip := noGrantRun[rt.name]; !skip && gm.audit == 0 {
				t.Fatalf("%s: the marked agent under a grant resolved no reference (%s): the case proves nothing; fix its config or list it in noReferenceCase", rt.name, shortErr(gm))
			}
			// agent access off: the value is nowhere, and the result is the marked agent's
			off, offWire := runRaw(t, inTurn, rt.name, mk, nil)
			base, baseWire := runRaw(t, marked, rt.name, mk, nil)
			for label, r := range map[string]secretRun{"in a turn": off, "marked agent": base} {
				if strings.Contains(r.shown(), inTurnSecret) || strings.Contains(offWire+baseWire, inTurnSecret) {
					t.Fatalf("%s, %s, access off: the value got out: %s", rt.name, label, r.shown())
				}
			}
			if (off.err == nil) != (base.err == nil) || off.audit != base.audit || off.audit != 0 || (!looseText[rt.name] && refusedText(off) != refusedText(base)) {
				t.Fatalf("%s access off: in a turn: %s refused=%v audit=%d; marked agent: %s refused=%v audit=%d", rt.name, shortErr(off), refusedText(off), off.audit, shortErr(base), refusedText(base), base.audit)
			}
			if _, skip := noGrantRun[rt.name]; skip {
				return
			}
			// agent access on: what a marked agent can do, and audited
			on, onWire := runRaw(t, inTurn, rt.name, mk, anyHost)
			if (on.err == nil) != (gm.err == nil) || on.audit != gm.audit || strings.Contains(onWire, inTurnSecret) != strings.Contains(gmWire, inTurnSecret) {
				t.Fatalf("%s access on: in a turn: %s audit=%d wire=%v; marked agent: %s audit=%d wire=%v", rt.name, shortErr(on), on.audit, strings.Contains(onWire, inTurnSecret), shortErr(gm), gm.audit, strings.Contains(gmWire, inTurnSecret))
			}
		})
	}
	// the stale half of noReferenceCase: an entry the table reaches now has to leave the list
	for _, rt := range types {
		reason, listed := noReferenceCase[rt.name]
		if !listed {
			continue
		}
		t.Run("listed/"+rt.name, func(t *testing.T) {
			g, _ := runRaw(t, marked, rt.name, func(p int) map[string]interface{} { return secretReaderConfig(rt.name, rt.pkg, p) }, anyHost)
			if g.audit != 0 {
				t.Fatalf("%s is listed in noReferenceCase (%s) but a marked agent under a grant resolved %d reference(s) here: remove the entry", rt.name, reason, g.audit)
			}
		})
	}
}

// Every registered type whose executor names where a connection's secret may go: a connection naming one host,
// a config naming another, from a process in a turn and from a marked agent: refused alike, nothing run.
func TestNodeRunInATurnConnectionDestinationsForEveryNodeThatHasThem(t *testing.T) {
	nested := map[string]string{}
	var ran int
	for _, rt := range registryTypes(t) {
		if rt.dest == nil {
			continue
		}
		key := ""
		for _, g := range rt.dest {
			if len(g) > 0 && !strings.Contains(g[0], ".") {
				key = g[0]
				break
			}
		}
		if key == "" {
			nested[rt.name] = "every destination group is a nested block with a credential_id of its own"
			continue
		}
		ran++
		t.Run(rt.name, func(t *testing.T) {
			for _, c := range []callerContext{callerContexts()[1], callerContexts()[5]} {
				dbPath := nodeVaultTestDB(t)
				c.setup(t)
				d := testDeps(dbPath, "default", false)
				d.ResolveCredential = func(_ context.Context, _ *connections.Store, k, _ string) (map[string]interface{}, error) {
					if k != "api-conn" {
						return nil, errors.New("no such connection")
					}
					return map[string]interface{}{key: "https://connection-host.example.test", "access_token": inTurnSecret, "password": inTurnSecret}, nil
				}
				raw, _ := json.Marshal(map[string]interface{}{"config": map[string]interface{}{key: "http://127.0.0.1:1/"}})
				out, err := runNodeSubcmdStdin(t, d, string(raw), rt.name, "--credential", "api-conn")
				if !errors.Is(err, workflow.ErrCredentialDestination) {
					t.Errorf("%s, %s: err = %v\n%s", rt.name, c.name, err, out)
				}
				if strings.Contains(out, inTurnSecret) || (err != nil && strings.Contains(err.Error(), inTurnSecret)) {
					t.Errorf("%s, %s: the value is in the output", rt.name, c.name)
				}
			}
		})
	}
	if ran < 15 {
		t.Fatalf("only %d node types with destinations ran: the registry walk is stale", ran)
	}
	t.Logf("destination case ran for %d node types; nested-only (not run): %v", ran, nested)
}

// secretReadingFiles are the only source files of the node packages that read the vault for a run's config. A new
// file that does has no in-turn case until it is added here AND to the table above.
var secretReadingFiles = map[string]string{
	"internal/nodes/automation_secrets.go": "browser nodes: listed in browserPackages",
	"internal/nodes/browserjev/values.go":  "browserjev: listed in browserPackages",
	"internal/nodes/db/helpers.go":         "every db-package node: the table runs each registered type of the package",
	"internal/nodes/mcp/node.go":           "mcp.call: the table and the mcp tests",
	"internal/nodes/http/request.go":       "http.request: the table and the http tests",
	"internal/nodes/vault/secret_get.go":   "vault.secret_get: the table",
}

var secretReadPattern = regexp.MustCompile(`secrets\.(Resolve|ResolveConfig|RefusedReference|DecryptFields|DecryptEntry|AuthorizeAgentReveal)\(|resolveSecret\(`)

func TestEverySourceFileThatReadsASecretHasAnInTurnCase(t *testing.T) {
	root := filepath.Join("..", "..", "..", "internal")
	found := map[string]bool{}
	for _, dir := range []string{"nodes", "apipkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if secretReadPattern.Match(b) {
				rel, _ := filepath.Rel(filepath.Join("..", "..", ".."), path)
				found[filepath.ToSlash(rel)] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for f := range found {
		if _, ok := secretReadingFiles[f]; !ok {
			t.Errorf("%s reads secrets for a node run but has no in-turn case: add one to in_turn_nodes_test.go (the reference table, or noReferenceCase / browserPackages with the reason) and list the file in secretReadingFiles", f)
		}
	}
	for f := range secretReadingFiles {
		if !found[f] {
			t.Errorf("secretReadingFiles lists %s, which no longer reads secrets: remove it", f)
		}
	}
}

// db.postgres (through a fake PostgreSQL server), db.mongodb and mcp.call (Streamable HTTP, a loopback server):
// the full list of callers. Agent access off: the value is nowhere; on: a marked agent's result, audited;
// the user's terminal resolves it.
func TestNodeRunInATurnDatabaseAndMCPNodes(t *testing.T) {
	for _, name := range []string{"db.postgres", "db.mongodb", "mcp.call"} {
		pkg := pkgDB
		if name == "mcp.call" {
			pkg = pkgMCP
		}
		mk := func(p int) map[string]interface{} { return secretReaderConfig(name, pkg, p) }
		gm, gmWire := runRaw(t, callerContexts()[5], name, mk, anyHost)
		if gm.audit == 0 {
			t.Fatalf("%s: the marked agent under a grant resolved nothing (%s)", name, shortErr(gm))
		}
		for _, c := range callerContexts() {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				if !c.agent {
					r, wire := runRaw(t, c, name, mk, nil)
					if refusedText(r) || r.audit != 0 {
						t.Fatalf("operator: %s refused=%v audit=%d", shortErr(r), refusedText(r), r.audit)
					}
					if name != "db.mongodb" && !strings.Contains(wire, inTurnSecret) {
						t.Fatalf("operator: the value did not reach the server (%d bytes): %s", len(wire), shortErr(r))
					}
					return
				}
				off, offWire := runRaw(t, c, name, mk, nil)
				if strings.Contains(off.shown()+offWire, inTurnSecret) || off.audit != 0 || !refusedText(off) {
					t.Fatalf("access off: value out=%v audit=%d refused=%v: %s", strings.Contains(off.shown()+offWire, inTurnSecret), off.audit, refusedText(off), shortErr(off))
				}
				on, onWire := runRaw(t, c, name, mk, anyHost)
				if on.audit != gm.audit || (on.err == nil) != (gm.err == nil) || strings.Contains(onWire, inTurnSecret) != strings.Contains(gmWire, inTurnSecret) {
					t.Fatalf("access on: %s audit=%d; marked agent: %s audit=%d", shortErr(on), on.audit, shortErr(gm), gm.audit)
				}
				if strings.Contains(on.shown(), inTurnSecret) {
					t.Fatalf("access on: the value is in the output: %s", on.shown())
				}
			})
		}
	}
}

// mcp.call over stdio: an unapproved step starts no program (workflow.ErrUnapprovedProgram), so a process in a
// turn gets the marked agent's refusal; a secret named in the server's env is refused first, by the same rule as
// everywhere else, and the value is nowhere.
func TestNodeRunInATurnMCPStdioStartsNoProgram(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]interface{}
		want string
	}{
		{"no secret in env", map[string]interface{}{"HOME": "/nonexistent"}, workflow.ErrUnapprovedProgram.Error()},
		{"secret in env", map[string]interface{}{"TOKEN": "@secret:vs"}, "is not read from the vault"},
	} {
		mk := func(int) map[string]interface{} {
			return map[string]interface{}{"transport": "stdio", "command": "/bin/true", "operation": "list_tools", "timeout": 2, "env": tc.env}
		}
		marked, _ := runRaw(t, callerContexts()[5], "mcp.call", mk, nil)
		if !strings.Contains(marked.shown(), tc.want) {
			t.Fatalf("%s: the marked agent's result is not %q: %s", tc.name, tc.want, shortErr(marked))
		}
		for _, c := range callerContexts() {
			if !c.agent {
				continue
			}
			t.Run(tc.name+"/"+c.name, func(t *testing.T) {
				r, _ := runRaw(t, c, "mcp.call", mk, nil)
				if !strings.Contains(r.shown(), tc.want) || strings.Contains(r.shown(), inTurnSecret) {
					t.Fatalf("not refused like a marked agent (%q): %s", tc.want, shortErr(r))
				}
			})
		}
	}
}
