// Package budget is the spend caps on AI tasks and the ledger they are held
// against: caps per kind, per conversation, per role and one global, the
// lowest applicable one winning, bounded above by the operator's coder budget.
// A cap of 0 means no spend, never unlimited, and a task starts only when it
// fits under every cap it falls in, decided in one statement so two tasks
// can't both take the last of a cap.
//
// Raising or changing a cap is the operator's (the CLI enforces that); an
// agent can only lower the cap of its own conversation (LowerCap).
package budget

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
)

// Scopes a cap can be set for.
const (
	ScopeGlobal       = "global"
	ScopeKind         = "kind"
	ScopeConversation = "conversation"
	ScopeRole         = "role"
)

// Error codes, besides aitask.CodeNoBudget.
const (
	CodeUnenforceable = "budget_unenforceable"
	CodeExceeded      = aitask.CodeBudgetExceeded
	CodeCapRaise      = "cap_raise_refused"
)

// epsilon absorbs float rounding when sums are compared with a cap.
const epsilon = 1e-9

// Ledger is the budgets of one database. Operator is the coder budget
// (`coder set --budget-usd`), the ceiling above every cap; 0 means the
// operator set none.
type Ledger struct {
	DB       *sql.DB
	Operator float64
	Now      func() time.Time
}

func (l *Ledger) now() string {
	t := time.Now
	if l.Now != nil {
		t = l.Now
	}
	return t().UTC().Format(time.RFC3339Nano)
}

// Cap is the cap that applies to a task: the lowest of the caps it falls in.
type Cap struct {
	USD float64 `json:"capUsd"`
	// Source names the cap that binds: "global", "kind:<k>", "conversation:<id>",
	// "role:<r>" or "operator" (the coder budget).
	Source string `json:"source"`
	// Set is false when nothing caps the task (no row, no operator budget): the
	// caller refuses it, since a missing cap is not an unlimited one.
	Set bool `json:"set"`
}

// Row is one stored cap.
type Row struct {
	Scope     string  `json:"scope"`
	Key       string  `json:"key"`
	CapUSD    float64 `json:"capUsd"`
	UpdatedBy string  `json:"updatedBy"`
	UpdatedAt string  `json:"updatedAt"`
}

func validScope(scope, key string) error {
	switch scope {
	case ScopeGlobal:
		if key != "" {
			return invalid("the global cap has no key")
		}
	case ScopeKind, ScopeConversation, ScopeRole:
		if strings.TrimSpace(key) == "" || len(key) > 200 || strings.ContainsAny(key, "\x00\r\n") {
			return invalid("a %s cap needs a key (the %s)", scope, scope)
		}
	default:
		return invalid("scope must be global, kind, conversation or role")
	}
	return nil
}

func invalid(format string, a ...any) error {
	return &aitask.Error{Code: aitask.CodeInvalidInput, Message: fmt.Sprintf(format, a...)}
}

func validUSD(usd float64) error {
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return invalid("a cap is a number of USD, 0 or more (0 means no spend)")
	}
	return nil
}

// Caps lists the stored caps.
func (l *Ledger) Caps() ([]Row, error) {
	rows, err := l.DB.Query(`SELECT scope, key, cap_usd, updated_by, updated_at FROM ai_budgets ORDER BY scope, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Scope, &r.Key, &r.CapUSD, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (l *Ledger) get(scope, key string) (float64, bool, error) {
	var v float64
	err := l.DB.QueryRow(`SELECT cap_usd FROM ai_budgets WHERE scope = ? AND key = ?`, scope, key).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return v, err == nil, err
}

// SetCap sets or changes a cap, up or down. It is the operator's: the caller
// has already checked that (an agent goes through LowerCap).
func (l *Ledger) SetCap(scope, key string, usd float64, by string) error {
	if err := validScope(scope, key); err != nil {
		return err
	}
	if err := validUSD(usd); err != nil {
		return err
	}
	_, err := l.DB.Exec(`INSERT INTO ai_budgets (scope, key, cap_usd, updated_by, updated_at) VALUES (?,?,?,?,?)
		ON CONFLICT(scope, key) DO UPDATE SET cap_usd = excluded.cap_usd, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		scope, key, usd, by, l.now())
	return err
}

// ClearCap removes a cap (the operator's).
func (l *Ledger) ClearCap(scope, key string) error {
	if err := validScope(scope, key); err != nil {
		return err
	}
	_, err := l.DB.Exec(`DELETE FROM ai_budgets WHERE scope = ? AND key = ?`, scope, key)
	return err
}

// LowerCap lowers the cap of a scope and never raises it: a new value above
// the current cap (the stored one, else the one that applies to kind) is
// refused. An agent uses it for its own conversation. A cap with nothing
// above it to lower is refused too, so it can't be used to set one.
func (l *Ledger) LowerCap(scope, key string, usd float64, kind, by string) error {
	if err := validScope(scope, key); err != nil {
		return err
	}
	if err := validUSD(usd); err != nil {
		return err
	}
	cur, ok, err := l.get(scope, key)
	if err != nil {
		return err
	}
	if !ok {
		conv := ""
		if scope == ScopeConversation {
			conv = key
		}
		eff, err := l.Resolve(kind, conv, "")
		if err != nil {
			return err
		}
		if !eff.Set {
			return &aitask.Error{Code: CodeCapRaise, Message: "no cap applies to this task yet, so there is nothing to lower; the operator sets caps"}
		}
		cur = eff.USD
	}
	if usd > cur+epsilon {
		return &aitask.Error{Code: CodeCapRaise,
			Message: fmt.Sprintf("a cap can only be lowered here: %v USD is above the current %v USD, and only the operator raises a cap", usd, cur)}
	}
	return l.SetCapMin(scope, key, usd, by)
}

// Resolve is the cap that applies to a task of kind in conversationID (role
// may be empty): the lowest of the global, kind, conversation and role caps
// and the operator's coder budget. With none of them it returns Set false.
func (l *Ledger) Resolve(kind, conversationID, role string) (Cap, error) {
	var out Cap
	consider := func(usd float64, source string) {
		if !out.Set || usd < out.USD {
			out = Cap{USD: usd, Source: source, Set: true}
		}
	}
	for _, c := range []struct{ scope, key string }{
		{ScopeGlobal, ""}, {ScopeKind, kind}, {ScopeConversation, conversationID}, {ScopeRole, role},
	} {
		if c.scope != ScopeGlobal && c.key == "" {
			continue
		}
		v, ok, err := l.get(c.scope, c.key)
		if err != nil {
			return Cap{}, err
		}
		if ok {
			source := c.scope
			if c.key != "" {
				source += ":" + c.key
			}
			consider(v, source)
		}
	}
	if l.Operator > 0 {
		consider(l.Operator, "operator")
	}
	return out, nil
}
