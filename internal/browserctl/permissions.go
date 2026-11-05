package browserctl

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Browser permissions (plan C8): a level per kind of agent, and per-site
// allow/block rules for all kinds, set by the user in Settings or with
// `browser permissions`. A human terminal is never restricted.

// PermissionsSetting is the configs-table row holding the permissions.
const PermissionsSetting = "browser_agent_permissions"

// Kinds of agent.
const (
	KindCoder = "coder" // coder chats and their dynorg workers
	KindApply = "apply" // the coder task that fills a job application (internal/applyfill)
	KindOrg   = "org"   // monomind org roles, org triggers
	KindChat  = "chat"  // the in-app chat and the workflow runs it starts
	KindOther = "other" // any other agent shell (Claude Code, Codex, … in a terminal)
)

// Kinds lists every kind, in the order the GUI shows them.
var Kinds = []string{KindCoder, KindApply, KindOrg, KindChat, KindOther}

// Levels, most to least access.
const (
	LevelFullNoApprovals = "full-no-approvals"
	LevelFull            = "full"
	LevelDev             = "dev"
	LevelAsk             = "ask"
	LevelOff             = "off"
)

// LevelInfo is one level as Settings shows it.
type LevelInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Catalog lists the levels with the label and one-line description the
// GUI shows verbatim.
func Catalog() []LevelInfo {
	return []LevelInfo{
		{LevelFullNoApprovals, "Full access, no approvals", "Any site; form submits go through without asking you, and scripts run on any page, where they can read that site's cookies and storage."},
		{LevelFull, "Full access", "Any site; a form submit on a real site waits for your approval."},
		{LevelDev, "Dev pages + allowed sites", "Localhost and dev pages, plus sites you allow; submits on real sites wait for your approval."},
		{LevelAsk, "Ask for new sites", "Like dev pages + allowed sites, but a new site asks you first instead of being refused."},
		{LevelOff, "Off", "This kind of agent can't use the browser."},
	}
}

// KindLabel is a kind's name in Settings and in messages.
func KindLabel(kind string) string {
	switch kind {
	case KindCoder:
		return "Coder chats"
	case KindApply:
		return "Application fill (coder task)"
	case KindOrg:
		return "Org roles"
	case KindChat:
		return "In-app chat"
	case KindOther:
		return "Other agents (terminal sessions)"
	}
	return kind
}

// DefaultLevels are the levels of kinds the user did not set: dev pages
// for every agent shell, as shipped in v0.141.0; the in-app chat keeps any
// site behind submit approvals, as it had.
//
// apply starts stricter, at ask, and can never be full-no-approvals: the
// submit of an application on a real site always waits for a person
// (levelAllowed).
var DefaultLevels = map[string]string{KindCoder: LevelDev, KindApply: LevelAsk, KindOrg: LevelDev, KindChat: LevelFull, KindOther: LevelDev}

// Site rules.
const (
	RuleAllow = "allow"
	RuleBlock = "block"
)

// SiteRule is one site's rule; Origin is in its canonical stored form
// (scheme://host[:port] or *.host, and for a block also a bare host). An
// allow admits exactly that origin (*.host: the host and its subdomains);
// a block covers the host and its subdomains on every scheme and port.
type SiteRule struct {
	Origin string `json:"origin"`
	Rule   string `json:"rule"`
}

// Permissions is the stored setting.
type Permissions struct {
	Levels map[string]string `json:"levels"`
	Sites  []SiteRule        `json:"sites"`
}

// DefaultPermissions has every kind at its default level and no site rules.
func DefaultPermissions() Permissions {
	p := Permissions{Levels: map[string]string{}, Sites: []SiteRule{}}
	for k, v := range DefaultLevels {
		p.Levels[k] = v
	}
	return p
}

func validLevel(level string) bool {
	for _, l := range Catalog() {
		if l.ID == level {
			return true
		}
	}
	return false
}

// levelAllowed reports whether kind may be set to level: apply never gets
// full-no-approvals, whoever asks.
func levelAllowed(kind, level string) bool {
	return !(kind == KindApply && level == LevelFullNoApprovals)
}

func validKind(kind string) bool {
	for _, k := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// LoadPermissions reads the setting; kinds it does not name have their
// default level. With no setting yet, the old allowlist (browser_agent_
// allowlist) is read as allow rules; the first SavePermissions keeps them.
func LoadPermissions(db *sql.DB) (Permissions, error) {
	raw, found, err := permissionsRow(db)
	if err != nil {
		return DefaultPermissions(), err
	}
	p, legacy, err := parsePermissions(db, raw, found)
	if err == nil && legacy {
		// Saved back with the schema, so it happens once and a choice made
		// after it is kept; a writer that got there first wins.
		if _, b, err := encodePermissions(p); err == nil {
			_, _ = db.Exec(`UPDATE configs SET config_data = ?, updated_at = CURRENT_TIMESTAMP
				WHERE name = ? AND config_data = ?`, b, PermissionsSetting, raw)
		}
	}
	return p, err
}

// permissionsSchema is the stored setting's version, written with it. A
// row without one may hold org=full, the default of builds before the
// release (which made dev the default for org roles): read once, org goes
// back to its default.
const permissionsSchema = 2

// storedPermissions is the setting as the configs row holds it.
type storedPermissions struct {
	Schema int               `json:"schema,omitempty"`
	Levels map[string]string `json:"levels"`
	Sites  []SiteRule        `json:"sites"`
}

// permissionsRow is the stored setting's text; found is false when there
// is none yet.
func permissionsRow(db *sql.DB) (raw string, found bool, err error) {
	err = db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, PermissionsSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading the browser permissions: %w", err)
	}
	return raw, true, nil
}

// parsePermissions reads the stored text (found: there is a row). legacy
// reports a row from before permissionsSchema whose org=full was reset.
func parsePermissions(db *sql.DB, raw string, found bool) (p Permissions, legacy bool, err error) {
	p = DefaultPermissions()
	if !found {
		old, err := loadOldAllowlist(db)
		if err != nil {
			return p, false, err
		}
		for _, o := range old {
			p.Sites = append(p.Sites, SiteRule{Origin: o, Rule: RuleAllow})
		}
		return p, false, nil
	}
	var stored storedPermissions
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return p, false, fmt.Errorf("reading the browser permissions: %w", err)
	}
	for k, v := range stored.Levels {
		if k == KindOrg && v == LevelFull && stored.Schema < permissionsSchema {
			legacy = true
			continue
		}
		if validKind(k) && validLevel(v) && levelAllowed(k, v) {
			p.Levels[k] = v
		}
	}
	for _, s := range stored.Sites {
		if c, err := canonSite(s); err == nil {
			p.Sites = mergeSite(p.Sites, c)
		}
	}
	return p, legacy, nil
}

// canonSite checks one rule and puts its origin in canonical form.
func canonSite(s SiteRule) (SiteRule, error) {
	if s.Rule != RuleAllow && s.Rule != RuleBlock {
		return s, fmt.Errorf("%s: a site rule is allow or block, not %q", s.Origin, s.Rule)
	}
	o, err := normalizeSiteEntry(s.Origin, s.Rule)
	if err != nil {
		return s, err
	}
	return SiteRule{Origin: o, Rule: s.Rule}, nil
}

// mergeSite adds s to sites, one rule per origin: a block wins over an
// allow stored under the same canonical origin.
func mergeSite(sites []SiteRule, s SiteRule) []SiteRule {
	for i, have := range sites {
		if have.Origin == s.Origin {
			if s.Rule == RuleBlock {
				sites[i] = s
			}
			return sites
		}
	}
	return append(sites, s)
}

// SavePermissions stores p (sites sorted, one rule per origin), replacing
// what is stored. A change to the stored permissions goes through
// UpdatePermissions, so a concurrent one is never lost.
func SavePermissions(db *sql.DB, p Permissions) error {
	_, b, err := encodePermissions(p)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`,
		PermissionsSetting, b)
	if err != nil {
		return fmt.Errorf("saving the browser permissions: %w", err)
	}
	return nil
}

// errPermissionsBusy: other writers kept changing the setting under
// UpdatePermissions.
var errPermissionsBusy = errors.New("the browser permissions kept changing while saving: try again")

// afterPermissionsRead runs between UpdatePermissions' read and its write
// (tests interleave a second writer there).
var afterPermissionsRead func()

// UpdatePermissions applies change to the stored permissions atomically:
// it reads them, applies change, and stores the result only if the row
// still holds what it read (a compare-and-set on the configs row), reading
// again when another writer got there first. It returns what it stored.
func UpdatePermissions(db *sql.DB, change func(*Permissions) error) (Permissions, error) {
	for range 50 {
		raw, found, err := permissionsRow(db)
		if err != nil {
			return Permissions{}, err
		}
		p, _, err := parsePermissions(db, raw, found)
		if err != nil {
			return p, err
		}
		if err := change(&p); err != nil {
			return p, err
		}
		out, b, err := encodePermissions(p)
		if err != nil {
			return p, err
		}
		if afterPermissionsRead != nil {
			afterPermissionsRead()
		}
		var res sql.Result
		if found {
			res, err = db.Exec(`UPDATE configs SET config_data = ?, updated_at = CURRENT_TIMESTAMP
				WHERE name = ? AND config_data = ?`, b, PermissionsSetting, raw)
		} else {
			res, err = db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at)
				VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) ON CONFLICT(name) DO NOTHING`, PermissionsSetting, b)
		}
		if err != nil {
			return p, fmt.Errorf("saving the browser permissions: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			return out, nil
		}
	}
	return Permissions{}, errPermissionsBusy
}

// encodePermissions checks p and returns its stored form and text (with
// permissionsSchema).
func encodePermissions(p Permissions) (Permissions, string, error) {
	out := DefaultPermissions()
	for k, v := range p.Levels {
		if !validKind(k) || !validLevel(v) {
			return out, "", fmt.Errorf("%s: %s is not a level for %s", PermissionsSetting, v, k)
		}
		if !levelAllowed(k, v) {
			return out, "", fmt.Errorf("%s: %s can never be %s: a submit on a real site always waits for a person", PermissionsSetting, k, v)
		}
		out.Levels[k] = v
	}
	for _, s := range p.Sites {
		c, err := canonSite(s)
		if err != nil {
			return out, "", err
		}
		out.Sites = mergeSite(out.Sites, c)
	}
	sort.Slice(out.Sites, func(i, j int) bool { return out.Sites[i].Origin < out.Sites[j].Origin })
	b, err := json.Marshal(storedPermissions{Schema: permissionsSchema, Levels: out.Levels, Sites: out.Sites})
	return out, string(b), err
}

// Level is kind's level.
func (p Permissions) Level(kind string) string {
	if l, ok := p.Levels[kind]; ok && levelAllowed(kind, l) {
		return l
	}
	return DefaultLevels[kind]
}

// SetLevel sets kind's level.
func (p *Permissions) SetLevel(kind, level string) error {
	if !validKind(kind) {
		return fmt.Errorf("%q is not a kind of agent: %s", kind, strings.Join(Kinds, ", "))
	}
	if !validLevel(level) {
		ids := []string{}
		for _, l := range Catalog() {
			ids = append(ids, l.ID)
		}
		return fmt.Errorf("%q is not a level: %s", level, strings.Join(ids, ", "))
	}
	if p.Levels == nil {
		p.Levels = map[string]string{}
	}
	if !levelAllowed(kind, level) {
		return fmt.Errorf("%s can never be %s: a submit on a real site always waits for a person", kind, level)
	}
	p.Levels[kind] = level
	return nil
}

// SetSite sets origin's rule (allow or block), or with "clear" removes it,
// and returns the origin as stored (canonical). found reports whether it
// had a rule.
func (p *Permissions) SetSite(origin, rule string) (stored string, found bool, err error) {
	if rule != RuleAllow && rule != RuleBlock && rule != "clear" {
		return "", false, fmt.Errorf("a site rule is allow, block or clear, not %q", rule)
	}
	o, err := normalizeSiteEntry(origin, rule)
	if err != nil {
		return "", false, err
	}
	kept := []SiteRule{}
	for _, s := range p.Sites {
		if s.Origin == o {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if rule != "clear" {
		kept = append(kept, SiteRule{Origin: o, Rule: rule})
	}
	p.Sites = kept
	return o, found, nil
}

// Rule is origin's stored rule ("" for none).
func (p Permissions) Rule(origin string) string {
	o, err := normalizeSiteEntry(origin, RuleBlock)
	if err != nil {
		return ""
	}
	for _, s := range p.Sites {
		if s.Origin == o {
			return s.Rule
		}
	}
	return ""
}

// Sited lists the origins with rule (allow or block).
func (p Permissions) Sited(rule string) []string {
	out := []string{}
	for _, s := range p.Sites {
		if s.Rule == rule {
			out = append(out, s.Origin)
		}
	}
	return out
}

// Blocked reports whether a block rule covers rawURL's host (an origin
// works too).
func (p Permissions) Blocked(rawURL string) bool {
	var be *SiteBlockedError
	return errors.As(Policy{Agent: true, Level: LevelFullNoApprovals, Blocklist: p.Sited(RuleBlock)}.Check(rawURL), &be)
}

// StricterKind is whichever of kinds a and b has the stricter level in p
// (a when they have the same one), for a caller that could be either.
func (p Permissions) StricterKind(a, b string) string {
	if strictness[p.Level(b)] > strictness[p.Level(a)] {
		return b
	}
	return a
}

// PolicyFor is the policy for an agent of kind ("" is a human terminal:
// unrestricted).
func (p Permissions) PolicyFor(kind string) Policy {
	return p.PolicyForCaller(Caller{Kind: kind})
}

// PolicyForCaller is the policy for c, at LevelOf(c).
func (p Permissions) PolicyForCaller(c Caller) Policy {
	if c.Kind == "" {
		return Policy{}
	}
	return Policy{Agent: true, Kind: c.Kind, Declared: c.Declared, Level: p.LevelOf(c), Allowlist: p.Sited(RuleAllow), Blocklist: p.Sited(RuleBlock)}
}

// Environment that tells kinds of agent apart.
const (
	EnvAgentKind = "MONOAGENT_AGENT_KIND" // "coder" (coder turns, dynorg workers), "apply" or "org"
	EnvAICaller  = "MONOAGENT_AI_CALLER"  // "chat": a run the in-app chat started
)

// Caller is who runs this process, for the browser permissions.
type Caller struct {
	// Kind is the kind of agent ("" for the user's own terminal): coder,
	// org, chat or other.
	Kind string `json:"kind"`
	// Declared is a kind (coder or org) the shell declared that no token
	// vouches for (no token, a bad one, its process gone, the records
	// unreadable). Kind is then other, and the stricter of the two kinds'
	// levels applies (LevelOf): a claim never loosens, and neither does a
	// lost token.
	Declared string `json:"declared,omitempty"`
}

// AgentCaller is the caller this process's environment shows. An agent
// shell (IsAgentContext) is the kind it declares — coder with
// MONOAGENT_AGENT_KIND=coder, org with MONOAGENT_AGENT_KIND=org or
// MONOMIND_ORG_ROLE — when a token its spawner issued vouches for it
// (IssueAgentKind), else other with that kind as Declared: a shell is
// never chat. chat is a process with MONOAGENT_AI_CALLER=chat and no shell
// markers. A process with none of those that the turn check (SetTurnVerdict)
// places inside a coder session's turn is other: its markers were stripped, and
// it is not the user's own terminal.
func AgentCaller() Caller {
	if IsAgentContext() {
		kind := declaredKind()
		if kind != "" && kindVouched(kind) {
			return Caller{Kind: kind}
		}
		return Caller{Kind: KindOther, Declared: kind}
	}
	if strings.TrimSpace(os.Getenv(EnvAICaller)) == KindChat {
		return Caller{Kind: KindChat}
	}
	if InTurn() {
		return Caller{Kind: KindOther}
	}
	return Caller{}
}

// AgentKind is AgentCaller's kind: "" for a human terminal.
func AgentKind() string { return AgentCaller().Kind }

// strictness orders the levels, the strictest highest. dev is stricter than
// ask: dev refuses a new site, ask lets the agent raise an approval for it.
var strictness = map[string]int{LevelFullNoApprovals: 0, LevelFull: 1, LevelAsk: 2, LevelDev: 3, LevelOff: 4}

// LevelOf is the level that binds c: its kind's, and with a kind it
// declared but could not prove, the stricter of that kind's level and its
// own (off, dev, ask, full, full-no-approvals, strictest first).
func (p Permissions) LevelOf(c Caller) string {
	l := p.Level(c.Kind)
	if c.Declared != "" {
		if d := p.Level(c.Declared); strictness[d] > strictness[l] {
			l = d
		}
	}
	return l
}

// declaredKind is the kind an agent shell's environment claims.
func declaredKind() string {
	switch kind := strings.TrimSpace(os.Getenv(EnvAgentKind)); {
	case kind == KindCoder || kind == KindApply || kind == KindOrg:
		return kind
	case strings.TrimSpace(os.Getenv("MONOMIND_ORG_ROLE")) != "":
		return KindOrg
	}
	return ""
}
