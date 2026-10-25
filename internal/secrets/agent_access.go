package secrets

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Agent access to a profile's vault. By default a run of a workflow the
// operator never approved (one the chat, an agent or another workflow wrote
// or changed) reads no @secret reference (RefuseResolving). The profile's
// owner can switch that refusal off for the profile; the switch is
// profile_agent_secret_access (migrations 110, 111), written only from the
// operator's own terminal (internal/cli/secret). It lifts that one refusal:
// a reference that came from a run's data or an expression stays refused
// (ErrDataReference). Standalone steps use the same grant; plaintext reads
// additionally require the reveal level.
//
// The switch can be narrowed to named secrets, to hosts a resolved secret
// may be sent to, and to a time. Every secret an unapproved run reads under
// it is written to profile_agent_secret_audit by name, never by value. The
// grant is read again at each resolution, so turning it off, or narrowing
// it, takes effect in runs already under way.

// AccessGrant is the switch as the owner set it.
type AccessGrant struct {
	Level     string     `json:"level"` // use or reveal; a missing grant is strict
	EnabledAt time.Time  `json:"enabled_at"`
	Secrets   []string   `json:"secrets,omitempty"`    // empty: every secret
	Hosts     []string   `json:"hosts,omitempty"`      // where a resolved secret may be sent
	AnyHost   bool       `json:"any_host"`             // no host binding
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil: never
}

// allowsSecret reports whether the grant covers the vault entry name.
func (g *AccessGrant) allowsSecret(name string) bool {
	if len(g.Secrets) == 0 {
		return true
	}
	for _, s := range g.Secrets {
		if s == name {
			return true
		}
	}
	return false
}

// allowsHost reports whether a resolved secret may be sent to the host of
// rawURL (a URL or a bare host[:port]).
func (g *AccessGrant) allowsHost(rawURL string) bool {
	if g.AnyHost {
		return true
	}
	host := HostOf(rawURL)
	if host == "" {
		return false
	}
	for _, h := range g.Hosts {
		if HostOf(h) == host {
			return true
		}
	}
	return false
}

// HostOf is the lowercase hostname (no port, no userinfo) of a URL or a
// bare host, "" when it names none.
func HostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// AllowResolving clears RefuseResolving's mark on ctx for a run under the
// profile's agent access: references resolve again, within the grant, and
// each is audited. origin names the run for the audit.
func AllowResolving(ctx context.Context, db *sql.DB, profileID string, origin AccessOrigin) context.Context {
	ctx = context.WithValue(ctx, refuseResolvingKey{}, "")
	return context.WithValue(ctx, accessKey{}, &accessState{db: db, profileID: profileID, origin: origin, seen: map[string]struct{}{}, names: map[string]struct{}{}, nodes: map[string]struct{}{}})
}

// AccessOrigin names the run an audit row belongs to.
type AccessOrigin struct {
	WorkflowID  string
	ExecutionID string
}

type accessKey struct{}
type accessNodeKey struct{}

type accessState struct {
	db        *sql.DB
	profileID string
	origin    AccessOrigin

	mu    sync.Mutex
	seen  map[string]struct{} // values resolved in this run, and their derived forms
	names map[string]struct{} // secrets read in this run, by name
	nodes map[string]struct{} // steps (by id) that resolved a secret in this run
	read  bool                // some step of this run (or of the run that seeded it) read a secret
}

type accessNode struct{ id, name, typ string }

// hostCheckedNodes are the node types that hold what they send to the
// grant's hosts (CheckSendHost). Under a host-bound grant no other node
// type reads a secret: it has no destination check, so the binding could
// not hold.
var hostCheckedNodes = map[string]bool{"http.request": true, "http.graphql": true, "mcp.call": true, "trigger.sse": true}

// WithAccessNodeType names the type of the step ctx runs. Under a grant that
// names hosts (not --any-host) only the types with a destination check may
// read secrets. A context with no type (no step) is not held to this.
func WithAccessNodeType(ctx context.Context, typ string) context.Context {
	n, _ := ctx.Value(accessNodeKey{}).(accessNode)
	n.typ = typ
	return context.WithValue(ctx, accessNodeKey{}, n)
}

// WithAccessNode names the step ctx runs, for the audit of secrets it reads.
func WithAccessNode(ctx context.Context, id, name string) context.Context {
	prev, _ := ctx.Value(accessNodeKey{}).(accessNode)
	return context.WithValue(ctx, accessNodeKey{}, accessNode{id: id, name: name, typ: prev.typ})
}

func accessOf(ctx context.Context) *accessState {
	a, _ := ctx.Value(accessKey{}).(*accessState)
	return a
}

// UnderAgentAccess reports whether ctx is a run that reads the vault only
// because the owner turned agent access on.
func UnderAgentAccess(ctx context.Context) bool { return accessOf(ctx) != nil }

// MinTrackedLen is the shortest secret value that is masked in stored
// outputs and bound to the grant's hosts: a shorter one would match
// ordinary text.
const MinTrackedLen = 6

// permit is the grant's say on reading secret name now; it fails closed.
func (a *accessState) permit(ctx context.Context, ref, name string) error {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), AgentTokenPrefix) {
		return &refusedError{ref + " is not read from the vault: agent tokens are never read by a run"}
	}
	g, err := GetAgentAccess(ctx, a.db, a.profileID)
	if err != nil {
		return &refusedError{ref + " is not read from the vault: agent access could not be checked (" + err.Error() + ")"}
	}
	if g == nil {
		return &refusedError{ref + " is not read from the vault: agent access was turned off or has expired"}
	}
	if !g.allowsSecret(name) {
		return &refusedError{ref + " is not read from the vault: agent access covers only " + strings.Join(g.Secrets, ", ")}
	}
	if !g.AnyHost {
		if n, _ := ctx.Value(accessNodeKey{}).(accessNode); n.typ != "" && !hostCheckedNodes[n.typ] {
			return &refusedError{ref + " is not read from the vault: agent access is bound to hosts (--host) and a " + n.typ + " step has no destination check; use --any-host to allow it"}
		}
	}
	return nil
}

// resolved audits the read of name and remembers value for masking. An audit
// that cannot be written withholds the value.
func (a *accessState) resolved(ctx context.Context, name, value string) error {
	n, _ := ctx.Value(accessNodeKey{}).(accessNode)
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO profile_agent_secret_audit (profile_id, at, workflow_id, execution_id, node_id, node_name, secret_name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.profileID, time.Now().UTC().Format(time.RFC3339Nano), a.origin.WorkflowID, a.origin.ExecutionID, n.id, n.name, name)
	if err != nil {
		return &refusedError{"@secret:" + name + " is not read from the vault: the audit of agent access could not be written (" + err.Error() + ")"}
	}
	a.track(name, value)
	a.mu.Lock()
	a.nodes[n.id] = struct{}{}
	a.mu.Unlock()
	return nil
}

// carries reports whether a send by the step ctx runs carries a secret: the
// run has read one (see below), the step resolved one (whatever form it took on the way out: a Basic header
// built from it, say), or one of texts holds a tracked value.
func (a *accessState) carries(ctx context.Context, texts []string) bool {
	n, _ := ctx.Value(accessNodeKey{}).(accessNode)
	a.mu.Lock()
	_, resolvedHere := a.nodes[n.id]
	read := a.read
	a.mu.Unlock()
	// Data flows between steps in forms nobody can list (a transformed echo
	// of the secret), so once any step of the run has read one every send of
	// the run is held to the grant's hosts.
	if resolvedHere || read {
		return true
	}
	for _, v := range a.values() {
		for _, t := range texts {
			if strings.Contains(t, v) {
				return true
			}
		}
	}
	return false
}

// track remembers value, and the forms a step commonly derives from it, for
// masking and the host binding. Best effort: an expression or code step can
// transform a value into a form that is not listed here.
func (a *accessState) track(name, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.names[name] = struct{}{}
	a.read = true
	for _, f := range derivedForms(value) {
		a.seen[f] = struct{}{}
	}
}

// derivedForms is v and its common encodings and case variants that are
// long enough to track.
func derivedForms(v string) []string {
	if len(v) < MinTrackedLen {
		return nil
	}
	j, _ := json.Marshal(v)
	b := []byte(v)
	forms := []string{
		v,
		base64.StdEncoding.EncodeToString(b), base64.URLEncoding.EncodeToString(b),
		base64.RawStdEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
		url.QueryEscape(v), url.PathEscape(v),
		string(j[1 : len(j)-1]),
		strings.NewReplacer("-", "_", "_", "-").Replace(v),
		strings.ReplaceAll(v, "-", "_"), strings.ReplaceAll(v, "_", "-"),
		hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)),
	}
	// Case variants only of a value that is not a plain word: the upper or
	// lower case of "secret" would mask ordinary text.
	if len(v) >= 12 || strings.IndexFunc(v, func(r rune) bool { return !unicode.IsLetter(r) }) >= 0 {
		forms = append(forms, strings.ToUpper(v), strings.ToLower(v))
	}
	seen := make(map[string]struct{}, len(forms))
	out := forms[:0]
	for _, f := range forms {
		if len(f) < MinTrackedLen {
			continue
		}
		if _, dup := seen[f]; !dup {
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	return out
}

// ResolvedNames are the names of the secrets read so far under ctx's agent
// access (sorted); nil when ctx is not under it. Names, never values: a
// resumed run or a sub-workflow re-reads them with SeedResolved.
func ResolvedNames(ctx context.Context) []string {
	a := accessOf(ctx)
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.names))
	for n := range a.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SeedResolved tracks the values of the named secrets for masking, as if
// ctx's run had read them (a sub-workflow's, or a resumed run's, earlier
// reads). It reads within the grant and is not audited again. A name the
// grant no longer covers, or that is gone or unreadable, is an error: its
// old value could not be masked, so the run does not start.
func SeedResolved(ctx context.Context, names []string) error {
	a := accessOf(ctx)
	if a == nil {
		return nil
	}
	for _, name := range names {
		if err := a.permit(ctx, secretRefPrefix+name, name); err != nil {
			return fmt.Errorf("secret %q read by the calling run can no longer be masked: %w", name, err)
		}
		v, err := resolveEntry(ctx, a.db, a.profileID, secretRefPrefix+name, name)
		if err != nil {
			return fmt.Errorf("secret %q read by the calling run can no longer be masked: %w", name, err)
		}
		a.track(name, v)
	}
	return nil
}

func (a *accessState) values() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.seen))
	for v := range a.seen {
		out = append(out, v)
	}
	// Longest first, so a value that holds another is masked whole.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// ResolvedValues are the secret values read so far under ctx's agent
// access (longest first); nil when ctx is not under it.
func ResolvedValues(ctx context.Context) []string {
	if a := accessOf(ctx); a != nil {
		return a.values()
	}
	return nil
}

// MaskResolved replaces every secret value read so far under ctx's agent
// access, wherever it occurs in s, with "***". Without agent access it
// returns s.
func MaskResolved(ctx context.Context, s string) string {
	for _, v := range ResolvedValues(ctx) {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// ErrHostNotAllowed is wrapped by CheckSendHost's refusal.
var ErrHostNotAllowed = fmt.Errorf("a secret from the vault goes only to a host agent access names")

// CheckSendHost refuses a send to rawURL that carries a secret read under
// agent access (one of texts holds its value) when the grant names hosts and
// rawURL's is not one. A send under no agent access, or one that carries no
// such secret, is not checked.
func CheckSendHost(ctx context.Context, rawURL string, texts ...string) error {
	a := accessOf(ctx)
	if a == nil {
		return nil
	}
	carries := a.carries(ctx, texts)
	if !carries {
		return nil
	}
	g, err := GetAgentAccess(ctx, a.db, a.profileID)
	if err != nil {
		return fmt.Errorf("%w: agent access could not be checked: %v", ErrHostNotAllowed, err)
	}
	if g == nil {
		return fmt.Errorf("%w: agent access was turned off or has expired", ErrHostNotAllowed)
	}
	if !g.allowsHost(rawURL) {
		return fmt.Errorf("%w: %q is not one of %s (secret agent-access on --host)", ErrHostNotAllowed, HostOf(rawURL), strings.Join(g.Hosts, ", "))
	}
	return nil
}

// CheckSendVia is CheckSendHost for a request that goes through a proxy or
// with certificate checks off: a request that carries a secret read under
// agent access may use the proxy only when the grant names its host, and
// never skips certificate checks unless the grant has no host binding.
func CheckSendVia(ctx context.Context, proxy *url.URL, insecure bool, texts ...string) error {
	if proxy != nil {
		if err := CheckSendHost(ctx, proxy.String(), texts...); err != nil {
			return fmt.Errorf("%w (proxy)", err)
		}
	}
	if !insecure {
		return nil
	}
	a := accessOf(ctx)
	if a == nil {
		return nil
	}
	if a.carries(ctx, texts) {
		g, err := GetAgentAccess(ctx, a.db, a.profileID)
		if err != nil || g == nil || !g.AnyHost {
			return fmt.Errorf("%w: a request that carries it does not skip certificate checks (allow_unauthorized_certs)", ErrHostNotAllowed)
		}
	}
	return nil
}

// AgentAccess reports whether the profile's owner let agent-written
// workflows read its vault (and the grant has not expired). A database with
// no such table (a test's schema) says no.
func AgentAccess(ctx context.Context, db *sql.DB, profileID string) (bool, error) {
	g, err := GetAgentAccess(ctx, db, profileID)
	return g != nil, err
}

// GetAgentAccess is the profile's current grant, nil when access is off or
// the grant has expired.
func GetAgentAccess(ctx context.Context, db *sql.DB, profileID string) (*AccessGrant, error) {
	if db == nil || profileID == "" {
		return nil, nil
	}
	var enabled, names, hosts, expires sql.NullString
	var level string
	var anyHost sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT CAST(enabled_at AS TEXT), secret_names, hosts, any_host, CAST(expires_at AS TEXT), level FROM profile_agent_secret_access WHERE profile_id = ?`,
		profileID).Scan(&enabled, &names, &hosts, &anyHost, &expires, &level)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	if level != "use" && level != "reveal" {
		return nil, fmt.Errorf("invalid vault agent access level %q", level)
	}
	g := &AccessGrant{Level: level, AnyHost: !anyHost.Valid || anyHost.Int64 != 0}
	g.EnabledAt, _ = parseTime(enabled.String)
	if names.Valid && names.String != "" {
		_ = json.Unmarshal([]byte(names.String), &g.Secrets)
	}
	if hosts.Valid && hosts.String != "" {
		_ = json.Unmarshal([]byte(hosts.String), &g.Hosts)
	}
	if expires.Valid && expires.String != "" {
		t, perr := parseTime(expires.String)
		if perr != nil {
			return nil, nil // an expiry nobody can read is off, not forever
		}
		if !time.Now().Before(t) {
			return nil, nil
		}
		g.ExpiresAt = &t
	}
	return g, nil
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable time %q", s)
}

// SetAgentAccess switches it on (every secret, any host, no expiry) or off
// for the profile. The caller has checked that this is the operator's own
// terminal.
func SetAgentAccess(ctx context.Context, db *sql.DB, profileID string, on bool) error {
	if on {
		return SetAgentAccessGrant(ctx, db, profileID, AccessGrant{AnyHost: true})
	}
	_, err := db.ExecContext(ctx, `DELETE FROM profile_agent_secret_access WHERE profile_id = ?`, profileID)
	return err
}

// SetAgentAccessGrant switches it on with g's scope, replacing any earlier
// grant of the profile.
func SetAgentAccessGrant(ctx context.Context, db *sql.DB, profileID string, g AccessGrant) error {
	if g.Level == "" {
		g.Level = "use"
	}
	if g.Level != "use" && g.Level != "reveal" {
		return fmt.Errorf("invalid vault agent access level %q", g.Level)
	}
	if g.Level == "reveal" && !g.AnyHost {
		return fmt.Errorf("reveal requires --any-host: plaintext cannot be bound to a destination")
	}
	names, _ := json.Marshal(g.Secrets)
	hosts, _ := json.Marshal(g.Hosts)
	var n, h interface{}
	if len(g.Secrets) > 0 {
		n = string(names)
	}
	if len(g.Hosts) > 0 {
		h = string(hosts)
	}
	var exp interface{}
	if g.ExpiresAt != nil {
		exp = g.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	anyHost := 0
	if g.AnyHost {
		anyHost = 1
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM profile_agent_secret_access WHERE profile_id = ?`, profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO profile_agent_secret_access (profile_id, enabled_at, secret_names, hosts, any_host, expires_at, level) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		profileID, time.Now().UTC().Format(time.RFC3339Nano), n, h, anyHost, exp, g.Level); err != nil {
		return err
	}
	return tx.Commit()
}

// AuditRow is one secret an unapproved run read under agent access.
type AuditRow struct {
	ID          int64  `json:"id"`
	At          string `json:"at"`
	WorkflowID  string `json:"workflow_id"`
	ExecutionID string `json:"execution_id"`
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	Secret      string `json:"secret"`
}

// AgentAccessAudit lists the profile's audit rows, oldest first.
func AgentAccessAudit(ctx context.Context, db *sql.DB, profileID string) ([]AuditRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, CAST(at AS TEXT), workflow_id, execution_id, node_id, node_name, secret_name FROM profile_agent_secret_audit WHERE profile_id = ? ORDER BY id`, profileID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.At, &r.WorkflowID, &r.ExecutionID, &r.NodeID, &r.NodeName, &r.Secret); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearAgentAccessAudit deletes the profile's audit rows and says how many.
// The caller has checked that this is the operator's own terminal.
func ClearAgentAccessAudit(ctx context.Context, db *sql.DB, profileID string) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM profile_agent_secret_audit WHERE profile_id = ?`, profileID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CanAgentReveal checks the current profile policy for a plaintext read.
// Internal authentication tokens are excluded even under Reveal.
func CanAgentReveal(ctx context.Context, db *sql.DB, profileID, name string) (bool, error) {
	g, err := GetAgentAccess(ctx, db, profileID)
	if err != nil {
		return false, err
	}
	return g != nil && g.Level == "reveal" && g.AnyHost && g.allowsSecret(name) && !IsReservedEntry(name), nil
}

// AuthorizeAgentReveal rechecks policy and records a name-only audit before
// plaintext is returned. Failure to write the audit withholds the value.
func AuthorizeAgentReveal(ctx context.Context, db *sql.DB, profileID, name string) error {
	ok, err := CanAgentReveal(ctx, db, profileID, name)
	if err != nil {
		return fmt.Errorf("checking vault reveal access: %w", err)
	}
	if !ok {
		return fmt.Errorf("vault plaintext access requires this profile's Reveal level for %q", name)
	}
	n, _ := ctx.Value(accessNodeKey{}).(accessNode)
	origin := AccessOrigin{}
	if a := accessOf(ctx); a != nil {
		origin = a.origin
	}
	_, err = db.ExecContext(ctx, `INSERT INTO profile_agent_secret_audit (profile_id, workflow_id, execution_id, node_id, node_name, secret_name) VALUES (?, ?, ?, ?, ?, ?)`, profileID, origin.WorkflowID, origin.ExecutionID, n.id, n.name, name)
	if err != nil {
		return fmt.Errorf("auditing vault reveal access: %w", err)
	}
	return nil
}
