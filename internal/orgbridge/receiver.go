package orgbridge

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/accountdoor"
	"github.com/monoes/mono-agent/internal/credfile"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// maxEndpointBody bounds one delivery (plan §8 layer 4).
const maxEndpointBody = 1 << 20

// maxReplyBytes bounds an automation's reply, like grant tool outputs.
const maxReplyBytes = 16 * 1024

// maxReplyAttempts bounds how often a failed reply is retried, so an org
// that stays unreachable cannot grow the append-only ledger forever.
const maxReplyAttempts = 5

// EndpointDelivery is what monomind M2 POSTs to an automation role.
type EndpointDelivery struct {
	OrgName   string `json:"orgName"`
	Run       string `json:"run"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	MessageID string `json:"messageId"`
}

type pendingDelivery struct {
	row      orggrant.EndpointRow
	d        EndpointDelivery
	deadline time.Time
}

// Receiver is the automation-role endpoint (plan §7.2): it authenticates a
// delivery by its capability id, confirms the message really went through
// the org — a delivery counts only once a bus event with the same messageId
// addressed to that role appears, so a role that read the id from the org
// file cannot POST around org_send and its fence (C-4) — then starts the
// role's workflow and, when that run ends, replies to the sender.
type Receiver struct {
	DB     *sql.DB
	Store  workflow.WorkflowStore
	Mux    *Mux
	RootOf func(profileID string) string
	Resume func(executionID string) error
	Logf   func(format string, args ...interface{})

	// VerifyWindow is how long a delivery may wait for its bus event.
	VerifyWindow time.Duration

	// Asks links token-less replies to waiting org.asks with Jev (nil:
	// a matcher over DB, which stays off until `jev enable asks`).
	Asks *AskMatcher

	mu         sync.Mutex
	pending    map[string]*pendingDelivery // messageId -> delivery
	seen       map[string]time.Time        // messageId|org:role -> bus event seen
	dispatched map[string]time.Time        // messageId -> dispatched
	subs       map[tailKey]func()
}

func (r *Receiver) init() {
	if r.pending == nil {
		r.pending = map[string]*pendingDelivery{}
		r.seen = map[string]time.Time{}
		r.dispatched = map[string]time.Time{}
		r.subs = map[tailKey]func(){}
	}
	if r.VerifyWindow <= 0 {
		r.VerifyWindow = time.Minute
	}
	if r.Logf == nil {
		r.Logf = func(string, ...interface{}) {}
	}
}

// Register mounts POST /org-endpoint/{id}. It has its own authentication
// and is served whether or not the API allows mutations.
func (r *Receiver) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /org-endpoint/{id}", r.ServeHTTP)
}

func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ServeHTTP handles one delivery. A locked monoes.me account refuses it first,
// before the endpoint is looked up or anything is recorded. A delivery accepted
// earlier whose bus event arrives after the lock is refused where every run is,
// in the engine.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := account.Require(req.Context()); err != nil {
		accountdoor.WriteUnauthorized(w, err)
		return
	}
	r.mu.Lock()
	r.init()
	r.mu.Unlock()
	ctx := req.Context()
	row, err := orggrant.NewStore(r.DB).LookupEndpoint(ctx, req.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "unknown endpoint"})
		return
	}
	if msg, status := checkEndpointAuth(req, row); status != 0 {
		writeJSONStatus(w, status, map[string]string{"error": msg})
		return
	}
	var d EndpointDelivery
	if err := json.NewDecoder(io.LimitReader(req.Body, maxEndpointBody)).Decode(&d); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "body must be a JSON delivery"})
		return
	}
	if d.OrgName != row.OrgName || (d.To != row.RoleID && d.To != row.OrgName+":"+row.RoleID) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "unknown endpoint"})
		return
	}
	if d.MessageID == "" || d.From == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "delivery needs from and messageId (monomind with capability org-endpoint-roles)"})
		return
	}

	r.mu.Lock()
	if _, done := r.dispatched[d.MessageID]; done {
		r.mu.Unlock()
		writeJSONStatus(w, http.StatusOK, map[string]interface{}{"accepted": true, "duplicate": true})
		return
	}
	r.ensureSubscribedLocked(row.ProfileID, row.OrgName)
	verified := r.seenLocked(d.MessageID, row.OrgName, row.RoleID)
	if !verified {
		r.pending[d.MessageID] = &pendingDelivery{row: *row, d: d, deadline: time.Now().Add(r.VerifyWindow)}
	} else {
		r.dispatched[d.MessageID] = time.Now()
	}
	r.mu.Unlock()

	if verified {
		r.dispatch(context.Background(), *row, d)
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]interface{}{"accepted": true, "verified": verified})
}

// checkEndpointAuth requires the bearer from the endpoint's credential file
// when one is set, and refuses non-loopback callers otherwise.
func checkEndpointAuth(req *http.Request, row *orggrant.EndpointRow) (string, int) {
	if row.CredentialFile != "" {
		info, err := os.Stat(row.CredentialFile)
		if err != nil {
			return "endpoint credential unavailable", http.StatusServiceUnavailable
		}
		if err := credfile.CheckOwnerOnly(row.CredentialFile, info); err != nil {
			if errors.Is(err, credfile.ErrInsecure) {
				return "endpoint credential file " + err.Error(), http.StatusServiceUnavailable
			}
			return "endpoint credential unavailable", http.StatusServiceUnavailable
		}
		want, err := os.ReadFile(row.CredentialFile)
		if err != nil {
			return "endpoint credential unavailable", http.StatusServiceUnavailable
		}
		got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(want))), []byte(got)) != 1 {
			return "missing or invalid bearer credential", http.StatusUnauthorized
		}
		return "", 0
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "non-loopback deliveries need a credential file on the endpoint", http.StatusForbidden
	}
	return "", 0
}

func seenKey(messageID, org, role string) string { return messageID + "|" + org + ":" + role }

func (r *Receiver) seenLocked(messageID, org, role string) bool {
	_, ok := r.seen[seenKey(messageID, org, role)]
	return ok
}

func (r *Receiver) ensureSubscribedLocked(profileID, org string) {
	root := r.RootOf(profileID)
	k := tailKey{root, org}
	if _, ok := r.subs[k]; ok {
		return
	}
	r.subs[k] = r.Mux.Subscribe(root, org, func(ev Event) { r.onEvent(org, ev) })
}

// subscribeLiveEndpoints starts a tail for every org that has a live
// automation-role endpoint. Subscribing lazily on the first POST loses that
// delivery's confirming bus event — Mux.Subscribe does not replay and
// monomind emits the event right after its 202 — so the first delivery to
// an org after each daemon start expired in Sweep as refused_grant and the
// automation never ran.
func (r *Receiver) subscribeLiveEndpoints(ctx context.Context) {
	r.mu.Lock()
	r.init()
	r.mu.Unlock()
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT profile_id, org_name FROM org_endpoints WHERE revoked_at IS NULL`)
	if err != nil {
		r.Logf("orgbridge: watching endpoint orgs: %v", err)
		return
	}
	type orgRef struct{ profileID, org string }
	var refs []orgRef
	for rows.Next() {
		var ref orgRef
		if err := rows.Scan(&ref.profileID, &ref.org); err == nil {
			refs = append(refs, ref)
		}
	}
	rows.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ref := range refs {
		r.ensureSubscribedLocked(ref.profileID, ref.org)
	}
}

// onEvent marks bus-confirmed messages and dispatches deliveries waiting
// for them.
func (r *Receiver) onEvent(org string, ev Event) {
	if (ev.Type != "message" && ev.Type != "xorg") || ev.MessageID() == "" || ev.To == "" {
		return
	}
	role := ev.To
	if i := strings.IndexByte(role, ':'); i >= 0 {
		if role[:i] != org {
			return
		}
		role = role[i+1:]
	}
	r.mu.Lock()
	r.init()
	r.seen[seenKey(ev.MessageID(), org, role)] = time.Now()
	p := r.pending[ev.MessageID()]
	if p != nil && p.row.OrgName == org && p.row.RoleID == role {
		delete(r.pending, ev.MessageID())
		r.dispatched[ev.MessageID()] = time.Now()
	} else {
		p = nil
	}
	r.mu.Unlock()
	if p != nil {
		r.dispatch(context.Background(), p.row, p.d)
	}
}

// Sweep refuses deliveries whose bus event never appeared, prunes old
// bookkeeping, and sends replies for finished automation runs.
func (r *Receiver) Sweep(ctx context.Context) {
	r.mu.Lock()
	r.init()
	now := time.Now()
	var expired []*pendingDelivery
	for id, p := range r.pending {
		if now.After(p.deadline) {
			expired = append(expired, p)
			delete(r.pending, id)
		}
	}
	for k, at := range r.seen {
		if now.Sub(at) > 10*time.Minute {
			delete(r.seen, k)
		}
	}
	for k, at := range r.dispatched {
		if now.Sub(at) > time.Hour {
			delete(r.dispatched, k)
		}
	}
	r.mu.Unlock()
	for _, p := range expired {
		r.Logf("orgbridge: endpoint %s:%s: refused message %s — no matching bus event (direct POST?)", p.row.OrgName, p.row.RoleID, p.d.MessageID)
		_ = NewLedger(r.DB).Refuse(ctx, Call{ProfileID: p.row.ProfileID, Direction: DirEndpointIn, OrgName: p.row.OrgName,
			RoleID: p.row.RoleID, WorkflowID: p.row.WorkflowID, EndpointID: p.row.ID}, StatusRefusedGrant)
	}
	if err := r.sendReplies(ctx); err != nil {
		r.Logf("orgbridge: replies: %v", err)
	}
}

// Run watches every org with a live automation-role endpoint and sweeps
// every few seconds until ctx ends.
func (r *Receiver) Run(ctx context.Context) {
	r.subscribeLiveEndpoints(ctx)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			for k, u := range r.subs {
				u()
				delete(r.subs, k)
			}
			r.mu.Unlock()
			return
		case <-t.C:
			r.Sweep(ctx)
		}
	}
}

// dispatch applies loop control and starts the automation role's workflow
// — or, when the message answers an org.ask, stores the reply instead.
func (r *Receiver) dispatch(ctx context.Context, row orggrant.EndpointRow, d EndpointDelivery) {
	ref := AskRef(d.Subject, d.Body)
	var linked *AskMatch
	if ref == "" {
		// No ask token: with `jev enable asks`, a confident Jev link to a
		// waiting ask stands in for it (plan WS9); otherwise today's path.
		if m, ok := askMatcherOr(r.Asks, r.DB, r.Logf).match(ctx, d.MessageID, row.ProfileID, row.OrgName, row.RoleID, d.Subject, d.Body); ok {
			ref, linked = m.AskID, &m
		}
	}
	if ref != "" {
		asks := NewAskStore(r.DB)
		if a, err := asks.Get(ctx, ref); err == nil && a != nil && a.OrgName == row.OrgName && a.EndpointRoleID == row.RoleID {
			reply := map[string]interface{}{"from": d.From, "subject": d.Subject, "body": StripTrace(d.Body)}
			if linked != nil {
				reply["_jev"] = linked.jevReplyMeta()
				logJevLink(r.Logf, row.OrgName, row.RoleID, d.MessageID, d.From, *linked)
			}
			if ok, _ := asks.MarkReplied(ctx, a.ID, reply); ok && r.Resume != nil {
				_ = r.Resume(a.ExecutionID)
			}
			return
		}
	}
	tr, _ := ParseTrace(d.Body)
	origin := row.OrgName
	if i := strings.IndexByte(d.From, ':'); i > 0 {
		origin = d.From[:i]
	}
	ledger := NewLedger(r.DB)
	adm, err := ledger.Admit(ctx, Call{
		ProfileID: row.ProfileID, Trace: tr, OriginOrg: origin, Direction: DirEndpointIn, OrgName: row.OrgName,
		RoleID: row.RoleID, WorkflowID: row.WorkflowID, EndpointID: row.ID, RunID: d.Run,
	}, Limits{})
	if err != nil {
		r.Logf("orgbridge: endpoint %s:%s: %v", row.OrgName, row.RoleID, err)
		return
	}
	if !adm.OK() {
		r.Logf("orgbridge: endpoint %s:%s: %s", row.OrgName, row.RoleID, adm.Reason)
		return
	}
	trigger := map[string]interface{}{
		"trigger_type": workflow.TriggerTypeOrgMessage,
		"org_message": map[string]interface{}{
			"org": row.OrgName, "run": d.Run, "from": d.From, "to": d.To, "role": row.RoleID,
			"subject": d.Subject, "body": StripTrace(d.Body), "message_id": d.MessageID,
		},
		"text":  StripTrace(d.Body),
		"trace": map[string]interface{}{"chain_id": adm.Trace.ChainID, "hop": adm.Trace.Hop},
	}
	// C-46: the run's file nodes confine to the sender's workdir.
	if r.RootOf != nil {
		if wd, ok := senderWorkdir(r.RootOf(row.ProfileID), row.OrgName, d.From); ok {
			trigger["org"] = map[string]interface{}{"name": row.OrgName, "role": row.RoleID, "workdir": wd}
		}
	}
	exec, err := workflow.CreateUnownedExecution(ctx, r.Store, workflow.UnownedExecutionOptions{
		WorkflowID: row.WorkflowID, ProfileID: row.ProfileID, TriggerType: workflow.TriggerTypeOrgMessage, AllowInactive: true,
		TriggerData: trigger,
	})
	if err != nil {
		_ = ledger.SetStatus(ctx, adm.ID, StatusError)
		r.Logf("orgbridge: endpoint %s:%s: start workflow: %v", row.OrgName, row.RoleID, err)
		return
	}
	_ = ledger.SetExecution(ctx, adm.ID, exec.ID)
}
