package mcpnode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// The test binary doubles as a fake stdio MCP server: TestMain runs
// runFakeStdio instead of the tests when fakeServerEnv is set.
const fakeServerEnv = "MCPNODE_FAKE_SERVER"

// fakeServer answers MCP requests; shared by the stdio and HTTP fakes.
type fakeServer struct {
	mu          sync.Mutex
	initialized bool
	calls       int
}

func rpcResult(id json.RawMessage, result interface{}) map[string]interface{} {
	return map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]interface{} {
	return map[string]interface{}{"jsonrpc": "2.0", "id": id, "error": map[string]interface{}{"code": code, "message": msg}}
}

func textContent(s string) []interface{} {
	return []interface{}{map[string]interface{}{"type": "text", "text": s}}
}

// handle returns the response to m, or nil for notifications and responses.
func (s *fakeServer) handle(m *rpcMessage) map[string]interface{} {
	if m.Method == "notifications/initialized" {
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
		return nil
	}
	if !m.isRequest() {
		return nil
	}
	s.mu.Lock()
	initialized := s.initialized
	s.mu.Unlock()
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.ProtocolVersion != ProtocolVersion || p.ClientInfo.Name != "monoagent" {
			return rpcErr(m.ID, -32602, "bad initialize params: "+string(m.Params))
		}
		return rpcResult(m.ID, map[string]interface{}{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "fake", "version": "1"},
		})
	case "tools/list":
		if !initialized {
			return rpcErr(m.ID, -32002, "not initialized")
		}
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Cursor == "" {
			return rpcResult(m.ID, map[string]interface{}{
				"tools": []interface{}{map[string]interface{}{
					"name": "echo", "description": "Echo the arguments",
					"inputSchema": map[string]interface{}{"type": "object"},
				}},
				"nextCursor": "page2",
			})
		}
		return rpcResult(m.ID, map[string]interface{}{"tools": []interface{}{
			map[string]interface{}{"name": "fail", "inputSchema": map[string]interface{}{"type": "object"}},
			map[string]interface{}{"name": "structured", "inputSchema": map[string]interface{}{"type": "object"}},
		}})
	case "tools/call":
		if !initialized {
			return rpcErr(m.ID, -32002, "not initialized")
		}
		s.mu.Lock()
		s.calls++
		s.mu.Unlock()
		var p struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch p.Name {
		case "echo":
			b, _ := json.Marshal(p.Arguments)
			return rpcResult(m.ID, map[string]interface{}{"content": textContent(string(b))})
		case "plain":
			return rpcResult(m.ID, map[string]interface{}{"content": textContent("hello there")})
		case "structured":
			return rpcResult(m.ID, map[string]interface{}{
				"content":           textContent(`{"n":42}`),
				"structuredContent": map[string]interface{}{"n": 42, "source": "structured"},
			})
		case "fail":
			return rpcResult(m.ID, map[string]interface{}{"content": textContent("boom"), "isError": true})
		case "env":
			return rpcResult(m.ID, map[string]interface{}{"content": textContent(os.Getenv("FAKE_TOKEN"))})
		case "run":
			return rpcResult(m.ID, map[string]interface{}{"content": textContent(os.Getenv("MONOAGENT_WORKFLOW_RUN"))})
		case "runtoken":
			return rpcResult(m.ID, map[string]interface{}{"content": textContent(os.Getenv("MONOAGENT_RUN_TOKEN"))})
		case "sleep":
			time.Sleep(30 * time.Second)
			return rpcResult(m.ID, map[string]interface{}{"content": textContent("late")})
		case "spawn":
			// Start a grandchild in the server's process group and record
			// both pids, so a test can check close reaps them.
			child := exec.Command("sleep", "60")
			if err := child.Start(); err != nil {
				return rpcErr(m.ID, -32000, err.Error())
			}
			pids := fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)
			_ = os.WriteFile(os.Getenv("FAKE_PIDFILE"), []byte(pids), 0o600)
			return rpcResult(m.ID, map[string]interface{}{"content": textContent(pids)})
		}
		return rpcErr(m.ID, -32602, "unknown tool "+p.Name)
	}
	return rpcErr(m.ID, -32601, "method not found")
}

// runFakeStdio serves MCP over stdin/stdout until stdin closes. Before
// answering a tools/call it sends a log notification and a ping request,
// which the client has to cope with.
func runFakeStdio() {
	s := &fakeServer{}
	out := bufio.NewWriter(os.Stdout)
	send := func(v interface{}) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	// A non-JSON line on stdout must be skipped by the client.
	fmt.Fprintln(out, "fake server starting")
	out.Flush()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	ping := 0
	for sc.Scan() {
		var m rpcMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "tools/call" {
			ping++
			send(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/message",
				"params": map[string]interface{}{"level": "info", "data": "calling"}})
			send(map[string]interface{}{"jsonrpc": "2.0", "id": "srv-" + strconv.Itoa(ping), "method": "ping"})
		}
		if resp := s.handle(&m); resp != nil {
			send(resp)
		}
	}
}
