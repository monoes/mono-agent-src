// Package mcp implements a stdio MCP (Model Context Protocol) server for
// AI agents: newline-delimited JSON-RPC 2.0 over os.Stdin/os.Stdout, with
// all logging on stderr so stdout stays a clean protocol channel.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/secrets"
)

// protocolVersion is the version this server answers a client that asks
// for none, or for one it does not know: the latest it supports. A client
// that asks for one of supportedProtocolVersions gets that one back.
const protocolVersion = "2025-06-18"

// supportedProtocolVersions are the MCP revisions this server speaks. What
// the later ones add (progress messages, structuredContent, outputSchema)
// are fields an earlier client ignores.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// negotiateProtocolVersion is the version initialize answers for params.
func negotiateProtocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &p) == nil && supportedProtocolVersions[p.ProtocolVersion] {
		return p.ProtocolVersion
	}
	return protocolVersion
}

// maxRequestLineBytes caps a single request line. A longer line is
// discarded and answered with a -32700 "request too large" error, but the
// server keeps serving subsequent requests (the connection survives).
const maxRequestLineBytes = 17 * 1024 * 1024

// postEOFGrace bounds how long in-flight handlers may keep running after
// stdin reaches EOF before the serve context is cancelled: fast tools
// finish and get their responses flushed; long waits (a workflow_run
// poll) are cut off by the cancel instead of holding the server open.
const postEOFGrace = 3 * time.Second

// Options configures the server.
type Options struct {
	// DBPath is the SQLite database path ("~/..." expanded).
	// Empty defaults to ~/.monoagent/monoagent.db.
	DBPath string
	// Profile is the profile a grant server (Grant set) serves: it must name the
	// grant's profile. A server that is not in grant mode holds no profile and
	// ignores it: every profile-scoped tool takes a required `profile` argument
	// and resolves it per call (profile_arg.go).
	Profile string
	// WorkflowsDir overrides the JSON workflow file store directory
	// (default ~/.monoagent/workflows; mainly for tests).
	WorkflowsDir string
	// Version is reported in initialize serverInfo.
	Version string
	// AllowMutations enables mutating tools (workflow_run, hil_approve/
	// reject, every create/update/delete-class monoagent tool, and
	// api_key_create/update/revoke, api_config_set/apply and api_auto_set).
	// When false (default), mutating tools are omitted from tools/list and
	// refuse with an explanatory error if called by name. Also settable via
	// MONOAGENT_MCP_ALLOW_MUTATIONS=="1". Mirrors internal/httpapi's
	// identically-named/shaped AllowMutations gate.
	AllowMutations bool
	// AllowAPIExposure lets api_config_set save a change that makes the
	// OpenAI-compatible API's server reach further than it did (a listener
	// beyond this machine, a higher confinement class, more runtimes: see
	// apiconfig.Widens). It is the operator's decision, made when the server
	// starts: no argument of any tool can set or lift it, because the model
	// sets the arguments. Without it such a change is refused and nothing is
	// saved. It is also what lets api_auto_set switch the auto model on, since
	// that sends prompts to TypeSafe: what leaves the machine is the
	// operator's decision too. It adds nothing to AllowMutations, which both
	// tools need first. Also settable via MONOAGENT_MCP_ALLOW_API_EXPOSURE=="1".
	AllowAPIExposure bool
	// APIOnly serves the OpenAI-compatible API's tools (api_*) and no other: no
	// workflow, vault, secret, person, org or documentation tool, so that a
	// model that is to manage the API through this server has nothing to run a
	// command with, which AllowMutations alone does not give (it also serves
	// workflow tools that can). It takes tools away and changes none of those
	// that stay: the mutating ones still need AllowMutations, and the gates of
	// api_config_set and api_auto_set are still AllowAPIExposure. Also
	// settable via MONOAGENT_MCP_API_ONLY=="1". Grant mode ignores it.
	APIOnly bool
	// TasksOnly serves the user's task board's tools (task_*) and no other: no
	// workflow, vault, secret, person, org, API or documentation tool, so that an
	// agent that is to work the board through this server has nothing to run a
	// command with, which AllowMutations alone does not give. It takes tools away
	// and changes none that stay: the verbs still need AllowMutations. Also
	// settable via MONOAGENT_MCP_TASKS_ONLY=="1". Serve refuses it together with
	// APIOnly; grant mode ignores it.
	TasksOnly bool
	// WorkflowTools serves saved workflows as tools wf_<slug> (workflow_tools.go):
	// "all" every workflow of the profile, otherwise a comma list of workflow IDs
	// or names; empty serves none. Also settable via MONOAGENT_MCP_WORKFLOW_TOOLS.
	WorkflowTools string
	// APIEnv is what the API tools (api_status, api_config_get/set/apply) read
	// of this process: its environment, the daemon's heartbeat, the service
	// manager (api_config_apply restarts the daemon through it) and the HTTP
	// probes. The zero value is the process's own, which is what the command
	// uses; tests give fakes, so that none of them runs a service manager or
	// depends on the machine. Its Environment is ignored: the documents of
	// this server always say "mcp".
	APIEnv apiconfig.Env
	// Grant, when set, serves grant mode: only the automations of that
	// grant's (org, role) bundle, for monomind's role tool provider. See
	// grant.go.
	Grant string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	// StructuredContent is the result again as a JSON object, for a tool
	// whose result is one (MCP 2025-06-18); the text block stays the same.
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

// Server dispatches MCP JSON-RPC requests to tool handlers.
type Server struct {
	opts Options
	rt   *runtime

	// rtMu guards the lazy runtime bootstrap: requests are dispatched on
	// separate goroutines, so concurrent tools/call invocations may race
	// to build the runtime on first use.
	rtMu sync.Mutex
	// epoch counts closeRuntime calls, under rtMu (see runtimeAt).
	epoch uint64

	// refusals rate-limits and tracks the ledger rows of never-served
	// grant-mode names (grant_refusal.go).
	refusals refusalLog

	// clientMu guards who this server serves (task_actor.go): the client's
	// name from initialize, the actor name the task tools fix from it, and
	// this server's own suffix. Requests run on goroutines of their own.
	clientMu    sync.Mutex
	clientName  string
	actorName   string
	actorSuffix string
}

// NewServer creates a Server with the given options.
func NewServer(opts Options) *Server {
	if !opts.AllowMutations {
		opts.AllowMutations = os.Getenv("MONOAGENT_MCP_ALLOW_MUTATIONS") == "1"
	}
	if !opts.AllowAPIExposure {
		opts.AllowAPIExposure = os.Getenv("MONOAGENT_MCP_ALLOW_API_EXPOSURE") == "1"
	}
	if !opts.APIOnly {
		opts.APIOnly = os.Getenv("MONOAGENT_MCP_API_ONLY") == "1"
	}
	if !opts.TasksOnly {
		opts.TasksOnly = os.Getenv("MONOAGENT_MCP_TASKS_ONLY") == "1"
	}
	if opts.WorkflowTools == "" {
		opts.WorkflowTools = os.Getenv("MONOAGENT_MCP_WORKFLOW_TOOLS")
	}
	return &Server{opts: opts, actorSuffix: newActorSuffix()}
}

// Run serves MCP over stdin/stdout until stdin closes. It is the entry
// point used by the `monoagentcli mcp` command.
func Run(opts Options) error {
	// stdin is the JSON-RPC stream: a file-keyring passphrase prompt must
	// never read from it.
	secrets.MarkStdinConsumed()
	return NewServer(opts).Serve(context.Background(), os.Stdin, os.Stdout)
}

// Serve reads newline-delimited JSON-RPC requests from in and writes one
// JSON response line per request to out. Notifications (requests with no
// raw "id" member) produce no output. Each request is dispatched on its
// own goroutine so a slow tool call cannot block other requests
// (head-of-line removal); responses are serialized through wmu, but their
// ORDER is no longer guaranteed under concurrency (acceptable per spec).
// When the read loop ends (EOF), the serve context is cancelled so any
// context-aware wait (e.g. a pending workflow_run polling loop) observes
// the closed client instead of blocking out its full timeout. It returns
// nil when in reaches EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	defer s.closeRuntime()
	// Asked for two narrow families, by flags or by the environment, a server
	// would serve neither: it refuses at once, reading nothing.
	if s.opts.Grant == "" && s.opts.TasksOnly && s.opts.APIOnly {
		return ErrTasksOnlyWithAPIOnly
	}
	if s.opts.Grant == "" && workflowToolsRequested(s.opts.WorkflowTools) && (s.opts.TasksOnly || s.opts.APIOnly) {
		return ErrWorkflowToolsWithNarrowMode
	}

	// serveCtx is cancelled as soon as the read loop ends (client closed
	// stdin) — handlers below receive it, so waitForExecution-style waits
	// return promptly instead of blocking for the full request timeout.
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader := bufio.NewReaderSize(in, 64*1024)
	w := bufio.NewWriter(out)
	// wmu serializes response writes (and the shared bufio.Writer) across
	// handler goroutines; writeErr remembers the first failure so Serve
	// can surface it after all in-flight handlers settle.
	var (
		wmu      sync.Mutex
		writeErr error
	)
	var wg sync.WaitGroup
	// notify writes one JSON-RPC notification (progress) between responses.
	notify := func(method string, params interface{}) {
		b, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "method": method, "params": params})
		if err != nil {
			return
		}
		wmu.Lock()
		defer wmu.Unlock()
		if _, werr := w.Write(append(b, '\n')); werr == nil {
			werr = w.Flush()
			if werr != nil && writeErr == nil {
				writeErr = werr
			}
		} else if writeErr == nil {
			writeErr = werr
		}
	}
	handlerCtx := withNotifier(serveCtx, notify)
	dispatch := func(line []byte) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := s.handleLine(handlerCtx, line)
			if resp == nil {
				return
			}
			wmu.Lock()
			defer wmu.Unlock()
			if werr := writeResponse(w, resp); werr != nil && writeErr == nil {
				writeErr = werr
			}
		}()
	}
	// settle stops reading and releases the handlers: in-flight work gets
	// postEOFGrace to complete on its own, then serveCtx is cancelled so
	// context-aware waits return instead of blocking out their timeouts.
	settle := func() {
		graceDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(graceDone)
		}()
		select {
		case <-graceDone:
		case <-time.After(postEOFGrace):
		}
		cancel()
		<-graceDone
	}

	for {
		if err := serveCtx.Err(); err != nil {
			settle()
			return err
		}
		line, tooLong, rerr := readLine(reader, maxRequestLineBytes)
		if tooLong {
			wmu.Lock()
			werr := writeResponse(w, &rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "request too large"},
			})
			wmu.Unlock()
			if werr != nil {
				settle()
				return werr
			}
		} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			dispatch(trimmed)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			settle()
			return fmt.Errorf("mcp: read stdin: %w", rerr)
		}
	}
	// stdin is gone: let in-flight handlers settle (bounded), then flush.
	settle()
	if writeErr != nil {
		return writeErr
	}
	return w.Flush()
}

// writeResponse marshals and writes one response line, flushing the writer
// so responses are not reordered or delayed by buffering.
func writeResponse(w *bufio.Writer, resp *rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: marshal response: %v\n", err)
		return nil
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.Flush()
}

// readLine reads one '\n'-terminated line, accumulating at most max bytes
// of line CONTENT (the '\n' delimiter itself is neither returned nor
// counted toward the limit). When the content exceeds max the remainder is
// still consumed (up to the delimiter) but discarded: it reports
// tooLong=true so the caller can answer with an error and continue
// serving. err is nil when the delimiter was found; io.EOF when input
// ended (possibly with a final unterminated line); any other error is a
// genuine read failure.
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, cerr := r.ReadSlice('\n')
		if cerr == nil {
			chunk = chunk[:len(chunk)-1]
		}
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if cerr != bufio.ErrBufferFull {
			return line, tooLong, cerr
		}
	}
}

// hasRequestID reports whether the request carries a usable (present,
// non-null) raw "id" member. Per JSON-RPC 2.0 a request without one is a
// notification: the server must not respond.
func hasRequestID(req rpcRequest) bool {
	id := bytes.TrimSpace(req.ID)
	return len(id) > 0 && !bytes.Equal(id, []byte("null"))
}
