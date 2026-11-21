// Package orgdecide is the org decision service (plan §7.7, U16/U17): it
// assigns every pending approval, question, gate, and org-started HIL item a
// tier, routes it by the org's autonomy level to a rule, a decider (a
// one-shot model, the org's boss, or a holding org's Initiator), or a human,
// resolves it through monomind, and records every routing in org_decisions.
// It enforces from the org_autonomy row, never from the org JSON, which an
// agent may be able to edit (C-54).
package orgdecide

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// Defaults (plan Q8, Q10, Q11).
const (
	DefaultDeciderRuntime = "claude"
	DefaultDeciderModel   = "claude-fable-5-1"
	DefaultDeciderTimeout = 120
	DefaultMaxDecisions   = 200
	DefaultMaxDeciderUSD  = 2.0
	// DefaultJevThreshold is the top-probability gate of a jev decider (plan
	// §5): below it the model decider decides.
	DefaultJevThreshold = 0.8
)

// Decider is the org_autonomy.decider_json shape.
type Decider struct {
	Kind           string `json:"kind"`
	Runtime        string `json:"runtime"`
	Model          string `json:"model"`
	Fallback       string `json:"fallback"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	// Threshold is the jev decider's gate on the top verdict's probability,
	// in (0,1]; 0 means DefaultJevThreshold. Other kinds ignore it.
	Threshold float64 `json:"threshold,omitempty"`
	// Provider is the jev decider's decision provider ("typesafe" or a
	// configured one, CheckDeciderProvider); "" follows the profile's
	// decider surface, else the machine default (plan D9). Other kinds
	// ignore it.
	Provider string `json:"provider,omitempty"`
}

// Limits is org_autonomy.limits_json.
type Limits struct {
	MaxDecisionsPerRun  int     `json:"max_decisions_per_run"`
	MaxDeciderUSDPerRun float64 `json:"max_decider_usd_per_run"`
}

// Autonomy is one org's enforced settings.
type Autonomy struct {
	ProfileID        string
	OrgName          string
	Level            string
	Decider          Decider
	Tiers            map[string]string
	Policy           string
	OnDeciderFailure string
	Limits           Limits
	PausedUntil      *time.Time
	UpdatedAt        time.Time
	UpdatedBy        string
	Stored           bool // false when no row exists and defaults were returned
}

// EffectiveLevel is the level decisions route by now: manual while paused.
func (a *Autonomy) EffectiveLevel(now time.Time) string {
	if a.PausedUntil != nil && a.PausedUntil.After(now) {
		return orgdesign.LevelManual
	}
	return a.Level
}

// Defaults returns the settings of an org with no row: manual, so orgs that
// predate autonomy behave exactly as before (U14, Q8).
func Defaults(profileID, org string) *Autonomy {
	return &Autonomy{
		ProfileID: profileID, OrgName: org, Level: orgdesign.LevelManual,
		Decider: Decider{Kind: orgdesign.DeciderModel, Runtime: DefaultDeciderRuntime, Model: DefaultDeciderModel,
			Fallback: orgdesign.DeciderModel, TimeoutSeconds: DefaultDeciderTimeout},
		Tiers:            map[string]string{},
		OnDeciderFailure: "deny",
		Limits:           Limits{MaxDecisionsPerRun: DefaultMaxDecisions, MaxDeciderUSDPerRun: DefaultMaxDeciderUSD},
	}
}

func (a *Autonomy) normalize() {
	if a.Decider.Kind == "" {
		a.Decider.Kind = orgdesign.DeciderModel
	}
	if a.Decider.Runtime == "" {
		a.Decider.Runtime = DefaultDeciderRuntime
	}
	if a.Decider.Model == "" {
		a.Decider.Model = DefaultDeciderModel
	}
	if a.Decider.Fallback == "" {
		a.Decider.Fallback = orgdesign.DeciderModel
	}
	if a.Decider.TimeoutSeconds <= 0 {
		a.Decider.TimeoutSeconds = DefaultDeciderTimeout
	}
	if a.Decider.Kind == orgdesign.DeciderJev && a.Decider.Threshold == 0 {
		a.Decider.Threshold = DefaultJevThreshold
	}
	if a.Tiers == nil {
		a.Tiers = map[string]string{}
	}
	if a.OnDeciderFailure == "" {
		a.OnDeciderFailure = "deny"
	}
	if a.Limits.MaxDecisionsPerRun <= 0 {
		a.Limits.MaxDecisionsPerRun = DefaultMaxDecisions
	}
	if a.Limits.MaxDeciderUSDPerRun <= 0 {
		a.Limits.MaxDeciderUSDPerRun = DefaultMaxDeciderUSD
	}
}

// Validate checks settings before they are stored.
func (a *Autonomy) Validate() error {
	var errs []string
	if orgdesign.LevelRank(a.Level) < 0 {
		errs = append(errs, fmt.Sprintf("level %q must be manual, mid, or full", a.Level))
	}
	if !orgdesign.ValidDeciderKind(a.Decider.Kind) {
		errs = append(errs, fmt.Sprintf("decider %q must be model, boss, parent, or jev", a.Decider.Kind))
	}
	if t := a.Decider.Threshold; t < 0 || t > 1 || math.IsNaN(t) {
		errs = append(errs, fmt.Sprintf("decider threshold %v must be in (0,1]", t))
	}
	if p := a.Decider.Provider; p != "" && !orgdesign.ValidDeciderProvider(p) {
		errs = append(errs, fmt.Sprintf("decider provider %q must be 1 to 32 of a-z, 0-9 and -", p))
	}
	if a.Decider.Fallback != orgdesign.DeciderModel {
		errs = append(errs, "decider fallback must be model")
	}
	for k, v := range a.Tiers {
		if !orgdesign.ValidDecisionClass(k) {
			errs = append(errs, fmt.Sprintf("%q is not a decision class", k))
		}
		if !orgdesign.ValidTier(v) {
			errs = append(errs, fmt.Sprintf("tier %q for %q must be routine, consequential, or irreversible", v, k))
		}
	}
	if a.OnDeciderFailure != "deny" && a.OnDeciderFailure != "human" {
		errs = append(errs, "on_decider_failure must be deny or human")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Store reads and writes org_autonomy and org_decisions.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db (migration 041 applied).
func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// Get returns an org's settings, or Defaults when it has no row.
func (s *Store) Get(ctx context.Context, profileID, org string) (*Autonomy, error) {
	var a Autonomy
	var decider, tiers, limits, updated string
	var paused sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT level, decider_json, tiers_json, policy_text, on_decider_failure, limits_json, paused_until, updated_at, updated_by
		 FROM org_autonomy WHERE profile_id = ? AND org_name = ?`, profileID, org).
		Scan(&a.Level, &decider, &tiers, &a.Policy, &a.OnDeciderFailure, &limits, &paused, &updated, &a.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Defaults(profileID, org), nil
	}
	if err != nil {
		return nil, fmt.Errorf("orgdecide: read autonomy: %w", err)
	}
	a.ProfileID, a.OrgName, a.Stored = profileID, org, true
	_ = json.Unmarshal([]byte(decider), &a.Decider)
	_ = json.Unmarshal([]byte(tiers), &a.Tiers)
	_ = json.Unmarshal([]byte(limits), &a.Limits)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if paused.Valid && paused.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, paused.String); err == nil {
			a.PausedUntil = &t
		}
	}
	a.normalize()
	return &a, nil
}

// Put stores settings, recording who changed them (cli | gui | chat |
// reconcile).
func (s *Store) Put(ctx context.Context, a *Autonomy, by string) error {
	a.normalize()
	if err := a.Validate(); err != nil {
		return err
	}
	decider, _ := json.Marshal(a.Decider)
	tiers, _ := json.Marshal(a.Tiers)
	limits, _ := json.Marshal(a.Limits)
	var paused interface{}
	if a.PausedUntil != nil {
		paused = a.PausedUntil.UTC().Format(time.RFC3339Nano)
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO org_autonomy (profile_id, org_name, level, decider_json, tiers_json, policy_text, on_decider_failure, limits_json, paused_until, updated_at, updated_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(profile_id, org_name) DO UPDATE SET level = excluded.level, decider_json = excluded.decider_json,
		   tiers_json = excluded.tiers_json, policy_text = excluded.policy_text, on_decider_failure = excluded.on_decider_failure,
		   limits_json = excluded.limits_json, paused_until = excluded.paused_until, updated_at = excluded.updated_at,
		   updated_by = excluded.updated_by`,
		a.ProfileID, a.OrgName, a.Level, string(decider), string(tiers), a.Policy, a.OnDeciderFailure, string(limits), paused,
		now.Format(time.RFC3339Nano), by)
	if err != nil {
		return fmt.Errorf("orgdecide: store autonomy: %w", err)
	}
	a.UpdatedAt, a.UpdatedBy, a.Stored = now, by, true
	return nil
}

// ListOrgs returns the orgs of a profile that have settings stored.
func (s *Store) ListOrgs(ctx context.Context, profileID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org_name FROM org_autonomy WHERE profile_id = ? ORDER BY org_name`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Verdicts.
const (
	VerdictApproved  = "approved"
	VerdictDenied    = "denied"
	VerdictAnswered  = "answered"
	VerdictEscalated = "escalated"
	VerdictFailed    = "failed"
)

// Decision is one org_decisions row.
type Decision struct {
	ID         string   `json:"id"`
	ProfileID  string   `json:"-"`
	OrgName    string   `json:"org"`
	RunID      string   `json:"run_id"`
	ItemKind   string   `json:"item_kind"`
	ItemRef    string   `json:"item_ref"`
	ItemHash   string   `json:"-"`
	Requester  string   `json:"requester"`
	Class      string   `json:"class"`
	Tier       string   `json:"tier"`
	Level      string   `json:"level"`
	Resolver   string   `json:"resolver"`
	Verdict    string   `json:"verdict"`
	AnswerText string   `json:"answer_text"`
	Rationale  string   `json:"rationale"`
	CostUSD    *float64 `json:"cost_usd"`
	LatencyMS  *int64   `json:"latency_ms"`
	ChainID    string   `json:"chain_id"`
	CreatedAt  string   `json:"created_at"`
	// Confidence and Probabilities are a jev decider's answer, kept even
	// when the model decided below the threshold (0/nil otherwise).
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Record inserts a decision row.
func (s *Store) Record(ctx context.Context, d *Decision) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if d.CreatedAt == "" {
		d.CreatedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	var confidence, probs interface{}
	if d.Confidence != 0 {
		confidence = d.Confidence
	}
	if len(d.Probabilities) > 0 {
		raw, err := json.Marshal(d.Probabilities)
		if err != nil {
			return fmt.Errorf("orgdecide: encode probabilities: %w", err)
		}
		probs = string(raw)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO org_decisions (id, profile_id, org_name, run_id, item_kind, item_ref, item_hash, requester, class, tier, level,
		   resolver, verdict, answer_text, rationale, cost_usd, latency_ms, chain_id, created_at, confidence, probabilities)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ProfileID, d.OrgName, nullable(d.RunID), d.ItemKind, d.ItemRef, d.ItemHash, nullable(d.Requester), d.Class, d.Tier,
		d.Level, d.Resolver, d.Verdict, nullable(d.AnswerText), nullable(d.Rationale), d.CostUSD, d.LatencyMS, nullable(d.ChainID), d.CreatedAt,
		confidence, probs)
	if err != nil {
		return fmt.Errorf("orgdecide: record decision: %w", err)
	}
	return nil
}

func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// DecisionFilter narrows List.
type DecisionFilter struct {
	RunID   string
	Verdict string
	Limit   int
}

// List returns an org's decisions, newest first.
func (s *Store) List(ctx context.Context, profileID, org string, f DecisionFilter) ([]Decision, error) {
	q := `SELECT id, org_name, COALESCE(run_id,''), item_kind, item_ref, item_hash, COALESCE(requester,''), class, tier, level, resolver,
	        verdict, COALESCE(answer_text,''), COALESCE(rationale,''), cost_usd, latency_ms, COALESCE(chain_id,''), created_at,
	        confidence, probabilities
	      FROM org_decisions WHERE profile_id = ? AND org_name = ?`
	args := []interface{}{profileID, org}
	if f.RunID != "" {
		q += ` AND run_id = ?`
		args = append(args, f.RunID)
	}
	if f.Verdict != "" {
		q += ` AND verdict = ?`
		args = append(args, f.Verdict)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		var d Decision
		var cost sql.NullFloat64
		var lat sql.NullInt64
		var conf sql.NullFloat64
		var probs sql.NullString
		if err := rows.Scan(&d.ID, &d.OrgName, &d.RunID, &d.ItemKind, &d.ItemRef, &d.ItemHash, &d.Requester, &d.Class, &d.Tier,
			&d.Level, &d.Resolver, &d.Verdict, &d.AnswerText, &d.Rationale, &cost, &lat, &d.ChainID, &d.CreatedAt, &conf, &probs); err != nil {
			return nil, err
		}
		d.Confidence = conf.Float64
		if probs.Valid && probs.String != "" {
			_ = json.Unmarshal([]byte(probs.String), &d.Probabilities)
		}
		if cost.Valid {
			d.CostUSD = &cost.Float64
		}
		if lat.Valid {
			d.LatencyMS = &lat.Int64
		}
		d.ProfileID = profileID
		out = append(out, d)
	}
	return out, rows.Err()
}

// LatestFor returns the newest decision for one item, or nil.
func (s *Store) LatestFor(ctx context.Context, profileID, org, kind, ref string) (*Decision, error) {
	var d Decision
	err := s.db.QueryRowContext(ctx,
		`SELECT id, level, resolver, verdict, created_at FROM org_decisions
		 WHERE profile_id = ? AND org_name = ? AND item_kind = ? AND item_ref = ? ORDER BY created_at DESC LIMIT 1`,
		profileID, org, kind, ref).Scan(&d.ID, &d.Level, &d.Resolver, &d.Verdict, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.ItemKind, d.ItemRef, d.OrgName = kind, ref, org
	return &d, nil
}

// RunUsage is decider use in one org run.
type RunUsage struct {
	DeciderDecisions int
	DeciderUSD       float64
}

// Usage sums decider-resolved rows (not rule or human) in a run.
func (s *Store) Usage(ctx context.Context, profileID, org, runID string) (RunUsage, error) {
	var u RunUsage
	var cost sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(cost_usd) FROM org_decisions
		 WHERE profile_id = ? AND org_name = ? AND COALESCE(run_id,'') = ? AND resolver NOT IN ('rule','human')`,
		profileID, org, runID).Scan(&u.DeciderDecisions, &cost)
	u.DeciderUSD = cost.Float64
	return u, err
}

// DeniedCount counts denials of the same request (item_hash) in a run.
func (s *Store) DeniedCount(ctx context.Context, profileID, org, runID, hash string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_decisions WHERE profile_id = ? AND org_name = ? AND COALESCE(run_id,'') = ? AND item_hash = ? AND verdict = 'denied'`,
		profileID, org, runID, hash).Scan(&n)
	return n, err
}
