package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/accountdoor"
	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/workflow"
)

// defaultAddr is the loopback-only default bind address, mirroring the
// webhook server's posture (internal/workflow/webhook_server.go): a
// same-user, same-machine trust boundary. Port 9321 is the webhook
// listener and 9323 the browser-extension bridge, so 9322 is used here.
const defaultAddr = "127.0.0.1:9322"

// fullOutputsHeader mirrors `workflow run --full-outputs`: when set to a
// truthy value, output items are truncated but not credential-key masked.
const fullOutputsHeader = "X-Full-Outputs"

// Server is the HTTP API server.
type Server struct {
	opts Options
	addr string
	mux  *http.ServeMux
	// handler is mux behind the account gate: what Handler and Serve use.
	handler http.Handler
	rt      *runtime
	// decoyToken is compared against when no profile matches the request, so
	// an unknown profile costs what a wrong token for a real one does.
	decoyToken string
	// readToken reads a profile's token from the vault without creating one
	// ("" when it has none). A field so that a test can count the reads.
	readToken func(ctx context.Context, profileID string) (string, error)
	// tokens is the agent-token store, built on first use (agentTokens).
	tokens     *agenttokens.Store
	tokensOnce sync.Once
	// routeTable is every route routes() registered, with the scope it needs.
	routeTable []routeClass
	// warnOut receives the start-up warning (default stderr).
	warnOut io.Writer
}

// NewServer builds a Server. The runtime (DB, profile, engine) is bootstrapped
// lazily on first request, exactly like the MCP server.
func NewServer(opts Options) (*Server, error) {
	opts.Addr = ResolveAddr(opts.Addr)
	if !opts.AllowMutations {
		opts.AllowMutations = os.Getenv("MONOAGENT_HTTPAPI_ALLOW_MUTATIONS") == "1"
	}
	if !opts.DisableProfileBearer {
		opts.DisableProfileBearer = os.Getenv("MONOAGENT_HTTPAPI_DISABLE_PROFILE_BEARER") == "1"
	}

	rt, err := newRuntime(opts)
	if err != nil {
		return nil, err
	}

	decoy, err := generateToken()
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("httpapi: %w", err)
	}
	s := &Server{opts: opts, addr: opts.Addr, rt: rt, decoyToken: decoy, warnOut: os.Stderr}
	s.readToken = func(ctx context.Context, profileID string) (string, error) {
		return existingToken(ctx, s.rt.db.DB, profileID)
	}
	s.mux = s.routes()
	s.handler = accountGate(s.mux)
	return s, nil
}

// ResolveAddr returns the address the server listens on: addr when set, else
// MONOAGENT_HTTPAPI_ADDR, else the loopback default. NewServer applies it,
// and so do callers that must know the address before they build the server.
func ResolveAddr(addr string) string {
	if addr == "" {
		addr = os.Getenv("MONOAGENT_HTTPAPI_ADDR")
	}
	if addr == "" {
		addr = defaultAddr
	}
	return addr
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.addr }

// AllowsMutations reports whether mutating endpoints are being served.
func (s *Server) AllowsMutations() bool { return s.opts.AllowMutations }

// Close releases the underlying runtime (engine + database).
func (s *Server) Close() { s.rt.Close() }

// Handler returns the server's http.Handler (account gate, auth and routing
// applied), mainly for tests that want to drive it with httptest without a
// real listener.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe binds s.Addr() and serves until ctx is cancelled, then
// gracefully shuts down. It is the entry point used by the `monoagentcli
// httpapi` command.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("httpapi: listen on %s: %w", s.addr, err)
	}
	return s.Serve(ctx, ln)
}

// standaloneWebhookWarning is what a standalone server says at start about
// webhook triggers. It starts a profile's engine on that profile's first run or
// activate request (read-only mode starts none), every engine wants the one
// webhook port, and whichever takes it serves only its own profile's triggers;
// the others retry every 30s. The daemon's engine serves every profile.
const standaloneWebhookWarning = "warning: this standalone HTTP API serves the webhook triggers of one profile at most: " +
	"a profile's engine starts on its first run or activate request here, and the first engine to find the webhook port " +
	"(MONOAGENT_WEBHOOK_ADDR, default 127.0.0.1:9321) free takes it, never while a daemon or another monoagent process holds it; " +
	"other profiles' webhook triggers are not served. `monoagentcli daemon` serves them all, and this same API (--api, on by default)"

// Serve serves on an already-bound listener until ctx is cancelled, then
// gracefully shuts down.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.ensureTokens(ctx)
	if !s.rt.hosted {
		fmt.Fprintln(s.warnOut, standaloneWebhookWarning)
	}
	extraHosts := configuredHosts(s.opts)
	if w := nonLoopbackWarning(ln.Addr().String(), extraHosts); w != "" {
		fmt.Fprintln(s.warnOut, w)
	}
	httpSrv := &http.Server{
		Handler:      hostGuard(allowedHosts(ln.Addr().String(), extraHosts), s.handler),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // workflow run waits can be long
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// routes builds the mux: read endpoints are always registered; mutating
// endpoints are only registered when AllowMutations is set — an
// unregistered path returns the mux's normal 404, not a 403, so no probe
// can distinguish "opted out" from "doesn't exist".
//
// Everything that acts on a profile lives under /profiles/{profile}/ and is
// authenticated with that profile's own token (auth.go). /health and /nodes*
// belong to the machine and stay global.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	// Every route of the API is registered through reg, which takes the scope the
	// route needs (or says that it is open): there is no way to add one that has no
	// class, and TestEveryRouteIsClassified keeps routes() from reaching for the mux.
	reg := &registrar{s: s, mux: mux}

	reg.open("GET /health", s.handleHealth) // unauthenticated

	reg.scoped("GET /profiles/{profile}/workflows", agenttokens.ScopeRead, s.handleWorkflowList)
	reg.scoped("GET /profiles/{profile}/workflows/{id}", agenttokens.ScopeRead, s.handleWorkflowGet)
	reg.scoped("GET /profiles/{profile}/workflows/{id}/executions", agenttokens.ScopeRead, s.handleWorkflowExecutions)
	reg.scoped("POST /profiles/{profile}/workflows/{id}/validate", agenttokens.ScopeWrite, s.handleWorkflowValidate)
	reg.scoped("GET /nodes", agenttokens.ScopeRead, s.handleNodeList)
	reg.scoped("GET /nodes/{type}/schema", agenttokens.ScopeRead, s.handleNodeSchema)
	reg.scoped("GET /profiles/{profile}/hil", agenttokens.ScopeRead, s.handleHilList)

	// The profile's documents, people and task board, for an agent that has no CLI
	// (agent mode): reads only, bounded and without a file path or a secret
	// (handlers_agent_data.go).
	reg.scoped("GET /profiles/{profile}/documents", agenttokens.ScopeRead, s.handleDocumentList)
	reg.scoped("GET /profiles/{profile}/documents/{id}", agenttokens.ScopeRead, s.handleDocumentGet)
	reg.scoped("GET /profiles/{profile}/documents/{id}/tags", agenttokens.ScopeRead, s.handleDocumentTagList)
	reg.scoped("GET /profiles/{profile}/people", agenttokens.ScopeRead, s.handlePeopleList)
	reg.scoped("GET /profiles/{profile}/people/{id}", agenttokens.ScopeRead, s.handlePersonGet)
	reg.scoped("GET /profiles/{profile}/tasks", agenttokens.ScopeRead, s.handleTaskList)
	reg.scoped("GET /profiles/{profile}/tasks/next", agenttokens.ScopeRead, s.handleTaskNext)
	reg.scoped("GET /profiles/{profile}/tasks/{id}", agenttokens.ScopeRead, s.handleTaskGet)

	// The AI task routes change nothing a token bound to a task could not: they are not behind the
	// mutation opt-in, which governs running workflows and approvals.
	s.registerAitaskRoutes(reg)

	if s.opts.AllowMutations {
		reg.scoped("POST /profiles/{profile}/workflows/{id}/run", agenttokens.ScopeRun, s.handleWorkflowRun)
		reg.scoped("POST /profiles/{profile}/workflows/{id}/activate", agenttokens.ScopeRun, s.handleWorkflowActivate)
		reg.scoped("POST /profiles/{profile}/workflows/{id}/deactivate", agenttokens.ScopeRun, s.handleWorkflowDeactivate)
		reg.scoped("POST /profiles/{profile}/hil/{id}/approve", agenttokens.ScopeFull, s.handleHilApprove)
		reg.scoped("POST /profiles/{profile}/hil/{id}/reject", agenttokens.ScopeFull, s.handleHilReject)
		// Tagging a document starts nothing and sends nothing, and the CLI does it
		// without an approval card.
		reg.scoped("POST /profiles/{profile}/documents/{id}/tags", agenttokens.ScopeWrite, s.handleDocumentTagAdd)
		reg.scoped("DELETE /profiles/{profile}/documents/{id}/tags/{tag}", agenttokens.ScopeWrite, s.handleDocumentTagRemove)
	}

	// The routes before profiles were explicit: refused for every method, with
	// a pointer to where they went. No credential is read and nothing is
	// looked up, so the answer is the same to everyone.
	for _, old := range []string{"/workflows", "/workflows/", "/hil", "/hil/"} {
		reg.open(old, handleRemovedRoute)
	}

	if s.opts.ExtraRoutes != nil {
		s.opts.ExtraRoutes(mux)
	}

	return mux
}

// registrar registers the API's routes on the mux and keeps the table of what
// each one needs.
type registrar struct {
	s   *Server
	mux *http.ServeMux
}

// scoped registers a route that needs a bearer: the profile's own (scope full) or
// an agent token whose scope allows need. A pattern with {profile} takes the
// token of that profile only; one without takes the token of any profile.
func (g *registrar) scoped(pattern string, need agenttokens.Scope, h http.HandlerFunc) {
	if !need.Valid() {
		panic("httpapi: route " + pattern + " has no scope class")
	}
	auth := g.s.anyProfileAuth
	if strings.Contains(pattern, "{profile}") {
		auth = g.s.profileAuth
	}
	g.mux.Handle(pattern, auth(need, refuseBoundToken(h)))
	g.s.routeTable = append(g.s.routeTable, routeClass{Pattern: pattern, Need: need})
}

// task registers a route that acts on one AI task. Unlike scoped, it is open to an agent token
// bound to that task (the handler checks the binding); every other route refuses a bound token.
func (g *registrar) task(pattern string, need agenttokens.Scope, h http.HandlerFunc) {
	if !need.Valid() || !strings.Contains(pattern, "{profile}") {
		panic("httpapi: task route " + pattern + " needs a scope class and a profile")
	}
	g.mux.Handle(pattern, g.s.profileAuth(need, h))
	g.s.routeTable = append(g.s.routeTable, routeClass{Pattern: pattern, Need: need, Task: true})
}

// open registers a route with no bearer at all: the health check and the answer to
// the routes that were removed. The test keeps the list of them.
func (g *registrar) open(pattern string, h http.HandlerFunc) {
	g.mux.HandleFunc(pattern, h)
	g.s.routeTable = append(g.s.routeTable, routeClass{Pattern: pattern, Open: true})
}

// routeClass is how one registered route authenticates.
type routeClass struct {
	Pattern string
	Need    agenttokens.Scope // the narrowest scope that may call it; empty for an open route
	Open    bool
	Task    bool // reachable by a token bound to a task (registrar.task)
}

// handleRemovedRoute answers the unprefixed /workflows and /hil routes, which
// acted on whichever profile the server had picked at startup.
func handleRemovedRoute(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": "this route was removed: workflows and HIL items are addressed per profile",
		"hint":  "/profiles/{profile}" + r.URL.Path,
	})
}

// authHeaderPrefix precedes the credential value in a standard HTTP
// Authorization header for this auth scheme.
const authHeaderPrefix = "Bearer "

// accountGate refuses every request but GET and HEAD /health, and the /v1 paths
// (whose gateway answers in the OpenAI envelope), while the account is locked.
// Routes registered through ExtraRoutes bring their own authentication, so auth
// cannot wrap them: this covers them, and a path the API does not have, so that a
// locked server's 404 tells nothing about which routes exist.
func accountGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !openWhileLocked(r) {
			if refusal := account.Require(r.Context()); refusal != nil {
				accountdoor.WriteUnauthorized(w, refusal)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// openWhileLocked reports whether the gate leaves r to the mux whatever the
// account says: the health check and the /v1 surface, which has its own door.
// It judges the escaped path, the one the mux routes and unescapes per segment:
// an encoded slash (/v1%2Fx) is no path under /v1 to the mux.
func openWhileLocked(r *http.Request) bool {
	switch p := r.URL.EscapedPath(); {
	case p == "/health":
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	case p == "/v1", strings.HasPrefix(p, "/v1/"):
		return true
	}
	return false
}

func bearerCredential(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) <= len(authHeaderPrefix) || h[:len(authHeaderPrefix)] != authHeaderPrefix {
		return ""
	}
	return h[len(authHeaderPrefix):]
}

// wantFullOutputs mirrors `workflow run --full-outputs`: passing
// X-Full-Outputs: 1 skips credential-key redaction of output items (the
// 4KB per-item truncation still applies). It carries the same trust
// implication as the CLI flag, so it is honoured only for the profile's own
// bearer (scope full): a scoped agent token never switches redaction off, and
// its header is ignored.
func wantFullOutputs(r *http.Request) bool {
	return r.Header.Get(fullOutputsHeader) == "1" && scopeOf(r) == agenttokens.ScopeFull
}

func redactItemsUnlessFull(r *http.Request, items []workflow.Item) []workflow.Item {
	if wantFullOutputs(r) {
		return workflow.TruncateItems(items)
	}
	return workflow.RedactAndTruncateItems(items)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "ok",
		"version":         s.opts.Version,
		"allow_mutations": s.opts.AllowMutations,
		"account":         accountdoor.SummaryOf(account.CurrentStatus()),
	})
}

// ── response helpers ─────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusForError maps a store/engine error to an HTTP status, mirroring
// the CLI's exit-code table (see AGENTS.md and cmd/monoagentcli/exitcodes.go):
// not-found -> 404, validation/invalid input -> 400, anything else -> 500.
// Handlers use this instead of always returning 500 so callers can branch
// on status the same way CLI callers branch on exit code.
func statusForError(err error) int {
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound), errors.Is(err, workflow.ErrExecutionNotFound):
		return http.StatusNotFound
	case errors.Is(err, workflow.ErrApprovalRequired):
		return http.StatusForbidden
	case errors.Is(err, workflow.ErrChangedSincePreview):
		return http.StatusConflict
	case errors.Is(err, workflow.ErrNoTriggerNode),
		errors.Is(err, workflow.ErrCycleDetected),
		errors.Is(err, workflow.ErrNodeTypeUnknown),
		errors.Is(err, workflow.ErrInvalidConfig),
		errors.Is(err, workflow.ErrWorkflowInactive):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeErrorFor(w http.ResponseWriter, err error) {
	writeError(w, statusForError(err), err.Error())
}
