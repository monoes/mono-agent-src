package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/accountdoor"
	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Authentication. One server serves every profile, and each profile has its
// own bearer token in its own vault (token.go). A request names its profile in
// the path (/profiles/{profile}/...), and the bearer is checked against the
// token of THAT profile only: a token is never an answer for another profile.
//
// A caller that fails to prove it holds the path profile's token gets one and
// the same 401 whatever the reason (no token, wrong token, another profile's
// token, a profile that does not exist, a name that two profiles share), so
// the response never tells an unauthenticated caller which profiles exist.

type profileCtxKey struct{}

// profileOf is the profile the request was authenticated for. Only handlers
// behind profileAuth may call it.
func profileOf(r *http.Request) string {
	id, _ := r.Context().Value(profileCtxKey{}).(string)
	return id
}

// A route declares the scope it needs: the narrowest scope of agent token that
// may call it (agenttokens.Scope). The profile's own bearer is scope full and
// reaches everything. A route with no class cannot be registered: profileAuth
// and anyProfileAuth take it as an argument.
//
//	read  GET only: list, get, status.
//	write mutations that start nothing and send nothing (validate; tagging a document).
//	run   starting or (de)activating a workflow.
//	full  approvals, and anything else that spends the profile's authority:
//	      vault secrets, sends and the account are never reachable below it.

// statusWriter records the status a handler wrote, for the audit log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// agentTokens is the store of scoped agent tokens, over the server's database.
func (s *Server) agentTokens() *agenttokens.Store {
	s.tokensOnce.Do(func() { s.tokens = agenttokens.NewStore(s.rt.db.DB) })
	return s.tokens
}

// scoped runs h for an agent token: refused with 403 when the token's scope is
// below need, and either way audited (name, method, escaped path, status; never the
// token). The audit row is written before h runs: a token whose use cannot be audited is
// refused with 503. The token's scope travels in the request context (see scopeOf).
func (s *Server) scoped(tok agenttokens.Token, need agenttokens.Scope, h http.HandlerFunc, w http.ResponseWriter, r *http.Request) {
	ctx := context.WithoutCancel(r.Context())
	auditID, err := s.agentTokens().BeginUse(ctx, tok, r.Method, r.URL.EscapedPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: %v\n", err)
		writeError(w, http.StatusServiceUnavailable, "this token's use cannot be audited, so the request is refused")
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		rec := recover()
		status := sw.status
		if rec != nil {
			status = http.StatusInternalServerError
		}
		if err := s.agentTokens().FinishUse(ctx, auditID, status); err != nil {
			fmt.Fprintf(os.Stderr, "httpapi: warning: %v\n", err)
		}
		if rec != nil {
			panic(rec)
		}
	}()
	if tok.Scope.Allows(need) {
		rctx := context.WithValue(r.Context(), scopeCtxKey{}, tok.Scope)
		h(sw, r.WithContext(context.WithValue(rctx, tokenCtxKey{}, tok)))
	} else {
		writeError(sw, http.StatusForbidden, fmt.Sprintf("this token's scope (%s) does not allow this request (needs %s)", tok.Scope, need))
	}
}

// auditForeignUse records a valid token used on a path that names another profile (or one
// that does not exist), as refused with 401: the caller is answered the one 401 of every
// credential failure, and the owner of the token sees the attempt in `token audit`. Both
// ways of getting here (a profile that exists and one that does not) do this one write, so
// the answer's cost does not tell them apart. A failed write is only logged: the request is
// refused either way.
func (s *Server) auditForeignUse(tok agenttokens.Token, r *http.Request) {
	ctx := context.WithoutCancel(r.Context())
	id, err := s.agentTokens().BeginUse(ctx, tok, r.Method, r.URL.EscapedPath())
	if err == nil {
		err = s.agentTokens().FinishUse(ctx, id, http.StatusUnauthorized)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: %v\n", err)
	}
}

type scopeCtxKey struct{}

// scopeOf is the scope the request was authenticated with: an agent token's, or full for
// the profile's own bearer (which sets none).
func scopeOf(r *http.Request) agenttokens.Scope {
	if sc, ok := r.Context().Value(scopeCtxKey{}).(agenttokens.Scope); ok {
		return sc
	}
	return agenttokens.ScopeFull
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer realm=monoagentcli-httpapi")
	writeError(w, http.StatusUnauthorized, "missing or invalid bearer credential")
}

// profileAuth wraps h with the profile-scoped bearer check. h reads the
// profile with profileOf.
func (s *Server) profileAuth(need agenttokens.Scope, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A locked account is refused before the vault is opened or the bearer is read.
		if err := account.Require(r.Context()); err != nil {
			accountdoor.WriteUnauthorized(w, err)
			return
		}
		got := bearerCredential(r)
		if got == "" {
			writeUnauthorized(w)
			return
		}
		ctx := r.Context()
		// One query whatever the path names (an id, a name, nothing that
		// exists), so the lookup's cost does not tell them apart either.
		candidates, decoy, err := profiledir.AuthCandidates(ctx, s.rt.db.DB, r.PathValue("profile"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "resolve profile: "+err.Error())
			return
		}
		if len(candidates) == 0 {
			// Do the work a real profile with a wrong token costs: the same
			// token-store read (listing the vault and decrypting the token of an
			// existing profile, which is discarded) and the same comparison, so
			// the latency does not tell unknown from wrong.
			if decoy != "" {
				_, _ = s.readToken(ctx, decoy)
			}
			constantTimeEqual(got, s.decoyToken)
			if strings.HasPrefix(got, agenttokens.Prefix) {
				if tok, err := s.agentTokens().Verify(ctx, got); err == nil {
					s.auditForeignUse(tok, r)
				}
			}
			writeUnauthorized(w)
			return
		}
		var matched []string
		for _, id := range candidates {
			want, err := s.readToken(ctx, id)
			if err == nil && want == "" {
				want, err = ensureToken(ctx, s.rt.db.DB, id)
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "resolve bearer credential: "+err.Error())
				return
			}
			if constantTimeEqual(got, want) {
				matched = append(matched, id)
			}
		}
		if s.opts.DisableProfileBearer {
			matched = nil // the tokens above were still read: the work stays the same
		}
		if len(matched) == 0 && strings.HasPrefix(got, agenttokens.Prefix) {
			if tok, err := s.agentTokens().Verify(ctx, got); err == nil {
				if containsID(candidates, tok.ProfileID) {
					s.scoped(tok, need, h, w, r.WithContext(context.WithValue(ctx, profileCtxKey{}, tok.ProfileID)))
					return
				}
				s.auditForeignUse(tok, r)
			}
		}
		switch {
		case len(matched) == 0:
			writeUnauthorized(w)
		case len(candidates) > 1:
			// The caller holds the token of one profile of that name: it may
			// be told the name is shared, and that the id is what to use.
			writeError(w, http.StatusConflict, "profile name is ambiguous: use the profile id")
		default:
			h(w, r.WithContext(context.WithValue(ctx, profileCtxKey{}, matched[0])))
		}
	})
}

// anyProfileAuth wraps h, a handler of a global route (/nodes), with a bearer
// check that accepts the token of any profile. It reads tokens, never creates
// them.
func (s *Server) anyProfileAuth(need agenttokens.Scope, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := account.Require(r.Context()); err != nil {
			accountdoor.WriteUnauthorized(w, err)
			return
		}
		got := bearerCredential(r)
		if got == "" {
			writeUnauthorized(w)
			return
		}
		ids, err := allProfileIDs(r.Context(), s.rt.db.DB)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "resolve profiles: "+err.Error())
			return
		}
		ok := constantTimeEqual(got, s.decoyToken) // false; keeps the work up when there are no tokens
		for _, id := range ids {                   // every token is compared: no early exit
			want, err := s.readToken(r.Context(), id)
			if err != nil {
				// One unreadable profile must not refuse a valid token of a
				// healthy one; with no match the answer is the uniform 401.
				lg := logging.Zerolog()
				lg.Warn().Err(err).Str("profile", id).Msg("httpapi: reading a profile's token failed; skipping it")
				continue
			}
			if want != "" && constantTimeEqual(got, want) {
				ok = true
			}
		}
		if s.opts.DisableProfileBearer {
			ok = false
		}
		if !ok && strings.HasPrefix(got, agenttokens.Prefix) {
			if tok, err := s.agentTokens().Verify(r.Context(), got); err == nil {
				s.scoped(tok, need, h, w, r)
				return
			}
		}
		if !ok {
			writeUnauthorized(w)
			return
		}
		h(w, r)
	})
}

func containsID(ids []string, id string) bool {
	for _, c := range ids {
		if c == id {
			return true
		}
	}
	return false
}

func allProfileIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ensureTokens gives every existing profile its token, so that `secret list`
// shows it from the first start. Best effort: a profile created later gets its
// token on its first request.
func (s *Server) ensureTokens(ctx context.Context) {
	if account.Require(ctx) != nil { // a locked server does not open the vault
		return
	}
	ids, err := allProfileIDs(ctx, s.rt.db.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: listing profiles: %v\n", err)
		return
	}
	for _, id := range ids {
		if id == profiledir.MachineProfileID {
			continue
		}
		if _, err := ensureToken(ctx, s.rt.db.DB, id); err != nil {
			fmt.Fprintf(os.Stderr, "httpapi: warning: profile %s: %v\n", id, err)
		}
	}
}
