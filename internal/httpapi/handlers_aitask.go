package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/aitask"
)

// The AI task routes: what a coding agent that has no database of its own needs to
// finish its task and read its own state. Each one calls the service layer the
// `aitask` commands call (internal/aitask), so the validation, the result schema, the
// kind's check and the single terminal state are the same on both paths.
//
// They are for scoped agent tokens only. The profile's own bearer is refused here, and a
// token acts on a task only when it is bound to that task and the conversation that runs
// it (agenttokens.Binding). complete, fail and cancel also need the task's own token, in
// the X-Aitask-Token header, exactly as the commands need MONOAGENT_AITASK_TOKEN: the API is
// not a way around it.

// aitaskTokenHeader carries the task's own token (MONOAGENT_AITASK_TOKEN).
const aitaskTokenHeader = "X-Aitask-Token"

const maxFailBody = 64 << 10

type tokenCtxKey struct{}

// agentTokenOf is the scoped agent token the request was authenticated with; ok is false for
// the profile's own bearer.
func agentTokenOf(r *http.Request) (agenttokens.Token, bool) {
	t, ok := r.Context().Value(tokenCtxKey{}).(agenttokens.Token)
	return t, ok
}

const aitaskBase = "/profiles/{profile}/agent/v1/aitask"

func (s *Server) registerAitaskRoutes(reg *registrar) {
	reg.task("GET "+aitaskBase+"/kinds", agenttokens.ScopeRead, s.handleAitaskKinds)
	reg.task("GET "+aitaskBase+"/{id}", agenttokens.ScopeRead, s.handleAitaskStatus)
	reg.task("GET "+aitaskBase+"/{id}/result", agenttokens.ScopeRead, s.handleAitaskResult)
	reg.task("POST "+aitaskBase+"/{id}/complete", agenttokens.ScopeWrite, s.handleAitaskComplete)
	reg.task("POST "+aitaskBase+"/{id}/fail", agenttokens.ScopeWrite, s.handleAitaskFail)
	reg.task("POST "+aitaskBase+"/{id}/cancel", agenttokens.ScopeWrite, s.handleAitaskCancel)
}

// refuseBoundToken wraps every route but the task routes: a token bound to one task reaches
// that task and nothing else of the profile (workflows, executions, approvals, documents,
// people, tasks), whatever its scope.
func refuseBoundToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := agentTokenOf(r); ok && tok.Bound() {
			writeError(w, http.StatusForbidden, "a token bound to a task reaches that task's routes only")
			return
		}
		h(w, r)
	}
}

// aitaskHTTPStatus maps a refusal code (Contract A) to a status.
func aitaskHTTPStatus(code string) int {
	switch code {
	case aitask.CodeNotFound:
		return http.StatusNotFound
	case aitask.CodeBadToken:
		return http.StatusForbidden
	case aitask.CodeTaskClosed, aitask.CodeNotQueued:
		return http.StatusConflict
	case aitask.CodeSchemaInvalid:
		return http.StatusUnprocessableEntity
	}
	return http.StatusBadRequest
}

// writeAitaskError answers with the refusal's code and fields, the same document the
// commands print with --json.
func writeAitaskError(w http.ResponseWriter, r *http.Request, err error) {
	redact := requestSecrets(r)
	var ae *aitask.Error
	if !errors.As(err, &ae) {
		writeError(w, http.StatusInternalServerError, "the AI task store failed: "+err.Error())
		return
	}
	body := map[string]any{}
	for k, v := range ae.Fields {
		body[k] = scrubValue(v, redact)
	}
	body["error"], body["code"] = redact(ae.Message), ae.Code
	writeJSON(w, aitaskHTTPStatus(ae.Code), body)
}

// requestSecrets hides both credentials of a request, the bearer and the task's own token, by
// value: in a stored result, a failure reason and anything an answer repeats.
func requestSecrets(r *http.Request) func(string) string {
	exact := aitask.RedactValues([]string{bearerCredential(r), r.Header.Get(aitaskTokenHeader)})
	return func(s string) string { return aitask.RedactAgentTokens(exact(s)) }
}

// taskTokenShape is a task token: 32 random bytes in hex (aitask.NewToken).
var taskTokenShape = regexp.MustCompile(`[0-9a-f]{64}`)

// readSecrets is requestSecrets for what a task's stored row says: it also hides every agent
// token whoever holds it, and any hex string that is task id's own token (checked against the
// stored hash, so no secret has to be presented for it to be found).
func readSecrets(r *http.Request, store *aitask.Store, id string) func(string) string {
	base := requestSecrets(r)
	return func(s string) string {
		s = base(s)
		return taskTokenShape.ReplaceAllStringFunc(s, func(c string) string {
			if store.CheckTokenRemote(id, c) {
				return aitask.RedactedValue
			}
			return c
		})
	}
}

// scrubValue applies redact to every string inside v (maps, lists and the strings themselves).
func scrubValue(v any, redact func(string) string) any {
	switch t := v.(type) {
	case string:
		return redact(t)
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = redact(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = scrubValue(e, redact)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[redact(k)] = scrubValue(e, redact)
		}
		return out
	}
	return v
}

// scrubTask hides both credentials in every text of a task that leaves the daemon, the decoded
// result included: what a stored row holds is not trusted to be free of them.
func scrubTask(t aitask.Task, redact func(string) string) aitask.Task {
	t.Goal, t.Error, t.ApplicationID = redact(t.Goal), redact(t.Error), redact(t.ApplicationID)
	if len(t.Result) > 0 {
		t.Result = json.RawMessage(aitask.ScrubJSON(t.Result, redact))
	}
	return t
}

// aitaskCaller is the agent token of a request, or a 403 already written.
func aitaskCaller(w http.ResponseWriter, r *http.Request) (agenttokens.Token, bool) {
	tok, ok := agentTokenOf(r)
	if !ok {
		writeError(w, http.StatusForbidden, "this route is for scoped agent tokens bound to a task, not the profile's own bearer")
	}
	return tok, ok
}

// aitaskFor resolves the {id} task for a request whose token is bound to it. Everything
// else is a 403 that does not say whether the task exists.
func (s *Server) aitaskFor(w http.ResponseWriter, r *http.Request) (*aitask.Service, aitask.Task, bool) {
	tok, ok := aitaskCaller(w, r)
	if !ok {
		return nil, aitask.Task{}, false
	}
	id := r.PathValue("id")
	if !tok.Bound() || tok.BoundTask != id {
		writeError(w, http.StatusForbidden, "this token is not bound to that task")
		return nil, aitask.Task{}, false
	}
	svc := &aitask.Service{Store: &aitask.Store{DB: s.rt.db.DB, Profile: profileOf(r)}, Remote: true}
	t, err := svc.Store.Get(id)
	if err != nil {
		writeAitaskError(w, r, err)
		return nil, aitask.Task{}, false
	}
	if tok.BoundConversation != t.ConversationID {
		writeError(w, http.StatusForbidden, "this token is not bound to that task")
		return nil, aitask.Task{}, false
	}
	return svc, t, true
}

// holderFor is aitaskFor plus the task's own token, which the request must present.
func (s *Server) holderFor(w http.ResponseWriter, r *http.Request) (*aitask.Service, string, bool) {
	svc, t, ok := s.aitaskFor(w, r)
	if !ok {
		return nil, "", false
	}
	token := r.Header.Get(aitaskTokenHeader)
	svc.Redact = requestSecrets(r)
	if !svc.Store.CheckTokenRemote(t.ID, token) {
		writeAitaskError(w, r, &aitask.Error{Code: aitask.CodeBadToken,
			Message: "this request does not carry task " + t.ID + "'s token (" + aitaskTokenHeader + "): only the task's own session may change it"})
		return nil, "", false
	}
	return svc, token, true
}

func (s *Server) handleAitaskKinds(w http.ResponseWriter, r *http.Request) {
	if _, ok := aitaskCaller(w, r); !ok {
		return
	}
	out := aitask.KindInfos()
	writeJSON(w, http.StatusOK, map[string]any{"kinds": out})
}

func (s *Server) handleAitaskStatus(w http.ResponseWriter, r *http.Request) {
	if _, t, ok := s.aitaskFor(w, r); ok {
		writeJSON(w, http.StatusOK, scrubTask(t, readSecrets(r, svcStore(s, r), t.ID)))
	}
}

func (s *Server) handleAitaskResult(w http.ResponseWriter, r *http.Request) {
	if _, t, ok := s.aitaskFor(w, r); ok {
		t = scrubTask(t, readSecrets(r, svcStore(s, r), t.ID))
		writeJSON(w, http.StatusOK, map[string]any{"taskId": t.ID, "status": t.Status, "result": t.Result, "error": t.Error})
	}
}

// handleAitaskComplete: the body is the result, the bytes `aitask complete --result FILE` reads.
func (s *Server) handleAitaskComplete(w http.ResponseWriter, r *http.Request) {
	svc, token, ok := s.holderFor(w, r)
	if !ok {
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, aitask.MaxResultBytes+1))
	if err != nil {
		writeAitaskError(w, r, &aitask.Error{Code: aitask.CodeInvalidInput, Message: "reading the result: " + err.Error()})
		return
	}
	t, err := svc.Complete(r.PathValue("id"), token, b)
	if err != nil {
		writeAitaskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scrubTask(t, readSecrets(r, svcStore(s, r), t.ID)))
}

func (s *Server) handleAitaskFail(w http.ResponseWriter, r *http.Request) {
	svc, token, ok := s.holderFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxFailBody)).Decode(&in); err != nil {
		writeAitaskError(w, r, &aitask.Error{Code: aitask.CodeInvalidInput, Message: "the body must be {\"reason\": TEXT}"})
		return
	}
	t, err := svc.Fail(r.PathValue("id"), token, in.Reason)
	if err != nil {
		writeAitaskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scrubTask(t, readSecrets(r, svcStore(s, r), t.ID)))
}

func (s *Server) handleAitaskCancel(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.holderFor(w, r)
	if !ok {
		return
	}
	t, err := svc.Cancel(r.PathValue("id"))
	if err != nil {
		writeAitaskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scrubTask(t, readSecrets(r, svcStore(s, r), t.ID)))
}

// svcStore is the profile's task store for a request.
func svcStore(s *Server, r *http.Request) *aitask.Store {
	return &aitask.Store{DB: s.rt.db.DB, Profile: profileOf(r)}
}
