package dynorg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The merge journal (note 13.3 section 3, R1): the begun merge is a sequence of rows in the run's MAC-chained
// audit stream, so a host restart finds every operation that was left open and a removed or reordered row breaks
// the chain. Kinds, in the order an operation writes them:
//
//	merge.begin     the claim: op, repo, branch, worker (written before any git command, after Register)
//	merge.step      step n is about to run (n = 2..4; step 1 is the begin row)
//	merge.pin       before, tip, ref, fingerprint of the chat folder (durable before the merge step)
//	merge.unproven  recovery could not prove the operation's processes gone (no repository access happened)
//	merge.end       the operation left CONTINUING: finished or incomplete (reason, repair)
//	merge.cleared   the operator cleared a needs-operator repair
const (
	kindBegin    = "merge.begin"
	kindStep     = "merge.step"
	kindPin      = "merge.pin"
	kindUnproven = "merge.unproven"
	kindEnd      = "merge.end"
	kindCleared  = "merge.cleared"
)

// Journal states, reasons and repairs (note 13.3 section 4.1).
const (
	StateFinished   = "finished"
	StateIncomplete = "incomplete"

	ReasonStepFailed   = "step-failed"
	ReasonBoundExpired = "bound-expired"
	ReasonHostDied     = "host-died"
	ReasonClaimLost    = "claim-lost"

	RepairClean             = "clean"
	RepairRolledBack        = "rolled-back"
	RepairAppliedUnrecorded = "applied-unrecorded"
	RepairNeedsOperator     = "needs-operator"
)

// mergeRec is the payload of every journal row; the fields a kind does not use are empty.
type mergeRec struct {
	Op     string `json:"op"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Worker string `json:"worker,omitempty"`
	Step   int    `json:"step,omitempty"`
	Before string `json:"before,omitempty"`
	Tip    string `json:"tip,omitempty"`
	Ref    string `json:"ref,omitempty"`
	FP     string `json:"fp,omitempty"`
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	Repair string `json:"repair,omitempty"`
	Note   string `json:"note,omitempty"`
}

func (r mergeRec) payload() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// JournalRow is one verified row of the journal.
type JournalRow struct {
	Generation int64
	Seq        int64
	Kind       string
	Rec        mergeRec
}

// MergeJournal reads the merge rows of every generation of a profile, in (generation, seq) order, after the
// chain of each generation verified. A chain that does not verify is an error: nothing is judged from a row that
// cannot be trusted (T24).
type MergeJournal interface {
	MergeRows(ctx context.Context, profile string) ([]JournalRow, error)
}

// SQLMergeJournal reads the audit table of the doomsday store. VerifyAudit is the store's chain check.
type SQLMergeJournal struct {
	DB          *sql.DB
	VerifyAudit func(ctx context.Context, profile string, generation int64) error
}

// MergeRows implements MergeJournal.
func (j SQLMergeJournal) MergeRows(ctx context.Context, profile string) ([]JournalRow, error) {
	if j.DB == nil || j.VerifyAudit == nil {
		return nil, errors.New("the merge journal is not wired")
	}
	gens, err := j.generations(ctx, profile)
	if err != nil {
		return nil, err
	}
	for _, g := range gens {
		if err := j.VerifyAudit(ctx, profile, g); err != nil {
			return nil, fmt.Errorf("the audit chain of generation %d does not verify, so no merge is judged from it: %w", g, err)
		}
	}
	rows, err := j.DB.QueryContext(ctx, `SELECT generation, seq, kind, payload FROM doomsday_audit WHERE profile = ? AND kind LIKE 'merge.%' ORDER BY generation, seq`, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalRow
	for rows.Next() {
		var r JournalRow
		var payload string
		if err := rows.Scan(&r.Generation, &r.Seq, &r.Kind, &payload); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(payload, "{") {
			continue // a row of the earlier text form: it names no operation
		}
		if err := json.Unmarshal([]byte(payload), &r.Rec); err != nil || r.Rec.Op == "" {
			return nil, fmt.Errorf("merge journal row %d/%d is malformed", r.Generation, r.Seq)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (j SQLMergeJournal) generations(ctx context.Context, profile string) ([]int64, error) {
	rows, err := j.DB.QueryContext(ctx, `SELECT DISTINCT generation FROM doomsday_audit WHERE profile = ? AND kind LIKE 'merge.%' ORDER BY generation`, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var g int64
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// opView is one operation folded from its rows.
type opView struct {
	rec       mergeRec // begin row: op, repo, branch, worker
	Gen       int64
	BeginSeq  int64
	Step      int // highest step row
	Pin       *mergeRec
	Ended     bool
	End       mergeRec
	Cleared   bool
	Unproven  bool // the last row is merge.unproven
	LastSeq   int64
}

// foldJournal folds rows into operations, keyed by op id, in begin order.
func foldJournal(rows []JournalRow) (ops map[string]*opView, order []string) {
	ops = map[string]*opView{}
	for _, r := range rows {
		o := ops[r.Rec.Op]
		if r.Kind == kindBegin {
			if o != nil {
				continue // a repeated begin never rewrites an operation
			}
			o = &opView{rec: r.Rec, Gen: r.Generation, BeginSeq: r.Seq, Step: 1}
			ops[r.Rec.Op] = o
			order = append(order, r.Rec.Op)
		}
		if o == nil {
			continue // a row for an operation with no begin row is not an operation of ours
		}
		if r.Kind == kindCleared {
			o.Cleared = true // the operator's row may be written in a later generation
			continue
		}
		if o.Gen != r.Generation {
			continue
		}
		o.LastSeq = r.Seq
		o.Unproven = false
		switch r.Kind {
		case kindStep:
			if r.Rec.Step > o.Step {
				o.Step = r.Rec.Step
			}
		case kindPin:
			p := r.Rec
			o.Pin = &p
		case kindUnproven:
			o.Unproven = true
		case kindEnd:
			if !o.Ended {
				o.Ended, o.End = true, r.Rec
			}
		}
	}
	return ops, order
}

// Open reports an operation with no end row: its member is live and it blocks its repository.
func (o *opView) Open() bool { return !o.Ended }

// NeedsOperator reports an ended operation whose repository the operator has not cleared.
func (o *opView) NeedsOperator() bool {
	return o.Ended && o.End.State == StateIncomplete && o.End.Repair == RepairNeedsOperator && !o.Cleared
}

// blocks reports whether the operation keeps a second merge out of its repository (T21).
func (o *opView) blocks() bool { return o.Open() || o.NeedsOperator() }
