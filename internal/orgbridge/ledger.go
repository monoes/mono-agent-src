package orgbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/dbconn"
)

// Crossing directions recorded in org_bridge_calls.
const (
	DirRoleTool      = "role_tool"      // a role called a granted automation
	DirEndpointIn    = "endpoint_in"    // a message reached an automation role
	DirWorkflowOut   = "workflow_out"   // a workflow node messaged or started an org
	DirEndpointReply = "endpoint_reply" // an automation role replied to its sender
	DirOrgStart      = "org_start"      // a holding org started a child
	DirWebhookIn     = "webhook_in"     // a signed trace came back in through a webhook
	DirOrgEvent      = "org_event"      // an org event started a trigger.org run on the event's chain
	DirEventStart    = "event_start"    // an org event started a trigger.org run on a fresh chain
)

// Call statuses.
const (
	StatusOK            = "ok"
	StatusRefusedHops   = "refused_hops"
	StatusRefusedRepeat = "refused_repeat"
	StatusRefusedGrant  = "refused_grant"
	StatusRefusedCap    = "refused_cap"
	StatusError         = "error"
)

// Ceilings on what an org may raise loop control to. run_config.max_hops
// and max_repeats come from the org JSON, which any role whose fileWrite
// reaches .monomind/ can edit (C-3, C-54), so a role could otherwise
// disarm U10 by writing a huge number into its own org file.
const (
	MaxHopsCeiling    = 32
	MaxRepeatsCeiling = 200
)

// SiblingCallsPerHop is how many granted calls on a chain, by any of its
// roles, add one hop (see Admit). Not settable from the org file.
const SiblingCallsPerHop = 8

// TriggerRepeats is the repeat limit for webhook_in and org_event, per
// target (the workflow) in Limits.Window, whatever the org's max_repeats
// says. Both start a run for every request or event, so without a limit a
// replayed signed token, or a role making tool calls in a tight loop, grows
// the table by a row each time. It is generous on purpose: a fan-out of one
// run's items to a local webhook, or an audit workflow on a busy role's
// tool events, is not a loop, and max_repeats' default of 20 would stop it.
const TriggerRepeats = 200

// Limits bound chains (U10). Zero fields take defaults; values above the
// ceilings are clamped.
type Limits struct {
	MaxHops    int           // default 8, at most MaxHopsCeiling
	MaxRepeats int           // same target within Window; default 20, at most MaxRepeatsCeiling
	Window     time.Duration // default 1 minute
}

func (l Limits) withDefaults() Limits {
	if l.MaxHops <= 0 {
		l.MaxHops = 8
	} else if l.MaxHops > MaxHopsCeiling {
		l.MaxHops = MaxHopsCeiling
	}
	if l.MaxRepeats <= 0 {
		l.MaxRepeats = 20
	} else if l.MaxRepeats > MaxRepeatsCeiling {
		l.MaxRepeats = MaxRepeatsCeiling
	}
	if l.Window <= 0 {
		l.Window = time.Minute
	}
	return l
}

// Call describes one crossing.
type Call struct {
	ProfileID   string
	Trace       Trace // header as received; Hop is advisory
	OriginOrg   string
	Direction   string
	OrgName     string
	RoleID      string
	WorkflowID  string
	ExecutionID string
	GrantID     string
	EndpointID  string
	RunID       string
}

// Admission is the outcome of Ledger.Admit.
type Admission struct {
	ID     string // org_bridge_calls row id; empty for a refusal not recorded (see Admit)
	Trace  Trace  // chain and the hop this crossing is at
	Status string // StatusOK or a refusal
	Reason string // human-readable refusal
}

// OK reports whether the crossing may proceed.
func (a Admission) OK() bool { return a.Status == StatusOK }

// Ledger is the org_bridge_calls audit and loop-control table.
type Ledger struct {
	db  *sql.DB
	now func() time.Time
}

// NewLedger returns a Ledger over db (migration 041 applied). A nil db
// gives a ledger that admits everything and records nothing — node tests
// and callers without a database.
func NewLedger(db *sql.DB) *Ledger { return &Ledger{db: db, now: time.Now} }

// Admit records one crossing and decides whether it may proceed. The hop
// is never lowered by the header a caller supplies — a role can write any
// trace line into a message — so hop = max(header hop, highest hop already
// recorded for the chain) + 1. A granted call leaves the chain's granted
// calls out of that maximum and adds one hop per SiblingCallsPerHop of them
// instead. The repeat limit counts crossings to the
// same target in the window regardless of chain, because a chain id can be
// forged fresh on every call but the target cannot.
func (l *Ledger) Admit(ctx context.Context, c Call, lim Limits) (Admission, error) {
	lim = lim.withDefaults()
	tr := c.Trace
	if tr.ChainID == "" {
		tr = NewTrace()
	}
	// A header hop is only ever raised to, never trusted lower; past the
	// ceiling it is refused anyway, so clamp it there before arithmetic: a
	// forged hop=MaxInt64 would otherwise wrap to a negative hop and pass.
	if tr.Hop > MaxHopsCeiling+1 {
		tr.Hop = MaxHopsCeiling + 1
	}
	if tr.Hop < 0 {
		tr.Hop = 0
	}
	if l.db == nil {
		return Admission{Trace: Trace{ChainID: tr.ChainID, Hop: tr.Hop + 1}, Status: StatusOK}, nil
	}
	// The reads that decide the hop and the repeat count, and the insert
	// that records the crossing, run in one IMMEDIATE transaction: without
	// it, parallel calls on one chain all read the same state and together
	// overshoot the limits (16 goroutines × 8 calls admitted ~76, not 64).
	// A concurrent Admit waits for it up to the database's busy_timeout.
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return Admission{}, fmt.Errorf("orgbridge: ledger: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		dbconn.AbandonBegin(conn)
		return Admission{}, fmt.Errorf("orgbridge: ledger: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// A fresh context, so the rollback runs even if ctx was cancelled.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	// Granted calls are not each a link of a loop: monomind gives a role
	// one chain for its whole run (roleTrace), changed only by a traced
	// message reaching it, so a busy role's calls pile up on one chain, and
	// so do the calls of roles that took turns on it. Counting each as a hop
	// refused them after max_hops calls with no loop in sight. So for
	// role_tool, every granted call on the chain (any role's) is left out
	// of the recorded maximum, and every SiblingCallsPerHop of them add one
	// hop instead. The depth still grows: a loop whose way back is never
	// recorded here (monomind's own org_send, a sync result) is refused
	// after MaxHops*SiblingCallsPerHop granted calls instead of MaxHops, and
	// one that comes back through a recorded crossing (the workflow's
	// workflow_out) climbs from that row as before. The per-grant call caps
	// (max_calls_per_run, max_calls_per_day) bound it too.
	//
	// Refused crossings do not set the chain's depth. A refused row records
	// a hop that never happened, and counting it let one forged crossing
	// (hop=999) kill a chain for every caller on it; a real loop refused at
	// hop N is still refused at hop N next time.
	//
	// org_event rows do not set the chain's depth either. monomind stamps
	// every tool event of a role with the role's chain, so an audit workflow
	// on a busy role's events is admitted once per event; if each of those
	// rows raised the depth the next one would climb, and the chain would
	// die after max_hops events with no loop in sight. A trigger.org run
	// that is part of a loop leaves the run through a crossing that is
	// recorded (workflow_out, a signed webhook_in, org_start), and that row
	// sets the depth. (An event_start row does count: it is the first row
	// of a fresh chain, one per chain, so nothing piles up on it.)
	q := `SELECT MAX(hop) FROM org_bridge_calls WHERE profile_id = ? AND chain_id = ? AND status NOT LIKE 'refused%'
	        AND direction <> 'org_event'`
	args := []interface{}{c.ProfileID, tr.ChainID}
	granted := 0
	if c.Direction == DirRoleTool {
		q += ` AND direction <> ?`
		args = append(args, DirRoleTool)
		if err := conn.QueryRowContext(ctx,
			// Refused calls never ran their automation: counting them let
			// one role's capped retries use up every role's allowance.
			`SELECT COUNT(*) FROM org_bridge_calls WHERE profile_id = ? AND chain_id = ? AND direction = ?
			   AND status NOT LIKE 'refused%'`,
			c.ProfileID, tr.ChainID, DirRoleTool).Scan(&granted); err != nil {
			return Admission{}, fmt.Errorf("orgbridge: ledger: %w", err)
		}
	}
	hop := tr.Hop
	if c.Direction == DirWebhookIn {
		// A webhook_in header was signed by the run that sent the request
		// (internal/tracesig), so its hop is the deeper of the hop that run
		// was started at and the hop its item reached on the chain
		// (workflow.RunTrace), and is trusted as is: the requests one run
		// sends (a fan-out over its items) all carry the same hop and are
		// siblings at hop+1. Taking the chain's recorded maximum here refused
		// a plain fan-out to a local webhook after a handful of items. A loop
		// climbs because every other leg is admitted through this ledger and
		// takes the recorded maximum, trigger.org runs included (org_event):
		// a loop R → tool event → trigger.org → webhook → back to R by a path
		// the ledger does not record starts each round's trigger.org run
		// above the previous round's webhook_in row.
		hop++
	} else {
		var recorded sql.NullInt64
		if err := conn.QueryRowContext(ctx, q, args...).Scan(&recorded); err != nil {
			return Admission{}, fmt.Errorf("orgbridge: ledger: %w", err)
		}
		if recorded.Valid && int(recorded.Int64) > hop {
			hop = int(recorded.Int64)
		}
		hop += 1 + granted/SiblingCallsPerHop
	}
	adm := Admission{ID: uuid.NewString(), Trace: Trace{ChainID: tr.ChainID, Hop: hop}, Status: StatusOK}

	if hop > lim.MaxHops {
		adm.Status = StatusRefusedHops
		adm.Reason = fmt.Sprintf("chain %s reached hop %d, over the limit of %d — this looks like a loop between orgs and automations", tr.ChainID, hop, lim.MaxHops)
	} else {
		maxRepeats := lim.MaxRepeats
		if triggerDirection(c.Direction) {
			maxRepeats = TriggerRepeats
		}
		since := l.now().Add(-lim.Window).UTC().Format(time.RFC3339Nano)
		var n int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM org_bridge_calls
			 WHERE profile_id = ? AND direction IN (?, ?) AND COALESCE(org_name,'') = ? AND COALESCE(role_id,'') = ?
			   AND COALESCE(workflow_id,'') = ? AND status = 'ok' AND created_at >= ?`,
			c.ProfileID, c.Direction, sameTarget(c.Direction), c.OrgName, c.RoleID, c.WorkflowID, since).Scan(&n); err != nil {
			return Admission{}, fmt.Errorf("orgbridge: ledger: %w", err)
		}
		if n >= maxRepeats {
			adm.Status = StatusRefusedRepeat
			adm.Reason = fmt.Sprintf("%d calls to the same target in the last %s (limit %d) — slow down or raise run_config.max_repeats", n, lim.Window, maxRepeats)
			if triggerDirection(c.Direction) {
				adm.Reason = fmt.Sprintf("%d runs of this workflow on org chains in the last %s (limit %d) — slow down", n, lim.Window, maxRepeats)
			}
		}
	}
	if !adm.OK() && triggerDirection(c.Direction) {
		// Only the first refusal of each kind per target in the window is
		// recorded: a row per refused replay of a token (at its hop limit or
		// over the repeat limit) would grow the table without bound, which
		// is what the limits are for.
		since := l.now().Add(-lim.Window).UTC().Format(time.RFC3339Nano)
		var refused int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM org_bridge_calls
			 WHERE profile_id = ? AND direction IN (?, ?) AND COALESCE(org_name,'') = ? AND COALESCE(role_id,'') = ?
			   AND COALESCE(workflow_id,'') = ? AND status = ? AND created_at >= ?`,
			c.ProfileID, c.Direction, sameTarget(c.Direction), c.OrgName, c.RoleID, c.WorkflowID, adm.Status, since).Scan(&refused); err != nil {
			return Admission{}, fmt.Errorf("orgbridge: ledger: %w", err)
		}
		if refused > 0 {
			adm.ID = ""
			return adm, nil
		}
	}
	if err := l.insertWith(ctx, conn, adm.ID, c, adm.Trace, adm.Status); err != nil {
		return Admission{}, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Admission{}, fmt.Errorf("orgbridge: ledger: commit: %w", err)
	}
	committed = true
	return adm, nil
}

// triggerDirection reports whether a crossing starts a workflow run for
// every request or event that arrives (webhook_in, and org_event /
// event_start for trigger.org), and so is repeat-limited by TriggerRepeats
// rather than the org's max_repeats.
func triggerDirection(dir string) bool {
	return dir == DirWebhookIn || dir == DirOrgEvent || dir == DirEventStart
}

// sameTarget is the other direction whose crossings count toward dir's
// repeat limit: a trigger.org run is one target whether it continued the
// event's chain or started a fresh one.
func sameTarget(dir string) string {
	switch dir {
	case DirOrgEvent:
		return DirEventStart
	case DirEventStart:
		return DirOrgEvent
	}
	return dir
}

// ChainOrigin returns the org the chain started in: the origin org of its
// first recorded crossing that has one, or "" when none has (a chain a
// webhook run started, or one not recorded at all).
func (l *Ledger) ChainOrigin(ctx context.Context, profileID, chainID string) (string, error) {
	if l.db == nil {
		return "", nil
	}
	var org string
	err := l.db.QueryRowContext(ctx,
		`SELECT origin_org FROM org_bridge_calls WHERE profile_id = ? AND chain_id = ? AND origin_org <> ''
		 ORDER BY rowid LIMIT 1`, profileID, chainID).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return org, err
}

// StartedChain reports whether chainID's first recorded crossing is an
// admitted event_start of workflowID: a trigger.org run of that workflow
// started the chain fresh (see TriggerSource.eventItems). Rows are ordered
// by rowid, the insertion order, not created_at, whose RFC 3339 text drops
// trailing zeros and so does not sort exactly.
func (l *Ledger) StartedChain(ctx context.Context, profileID, chainID, workflowID string) (bool, error) {
	if l.db == nil {
		return false, nil
	}
	var dir, wf, status string
	err := l.db.QueryRowContext(ctx,
		`SELECT direction, COALESCE(workflow_id,''), status FROM org_bridge_calls
		 WHERE profile_id = ? AND chain_id = ? ORDER BY rowid LIMIT 1`, profileID, chainID).Scan(&dir, &wf, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && dir == DirEventStart && wf == workflowID && status == StatusOK, err
}

// Refuse records a crossing refused before admission (grant revoked, cap
// reached).
func (l *Ledger) Refuse(ctx context.Context, c Call, status string) error {
	if l.db == nil {
		return nil
	}
	tr := c.Trace
	if tr.ChainID == "" {
		tr = NewTrace()
	}
	return l.insert(ctx, uuid.NewString(), c, tr, status)
}

// SetStatus updates a recorded crossing (e.g. to error after admission).
func (l *Ledger) SetStatus(ctx context.Context, id, status string) error {
	if l.db == nil {
		return nil
	}
	_, err := l.db.ExecContext(ctx, `UPDATE org_bridge_calls SET status = ? WHERE id = ?`, status, id)
	return err
}

// SetExecution attaches the execution a crossing started.
func (l *Ledger) SetExecution(ctx context.Context, id, executionID string) error {
	if l.db == nil {
		return nil
	}
	_, err := l.db.ExecContext(ctx, `UPDATE org_bridge_calls SET execution_id = ? WHERE id = ?`, executionID, id)
	return err
}

func (l *Ledger) insert(ctx context.Context, id string, c Call, tr Trace, status string) error {
	return l.insertWith(ctx, l.db, id, c, tr, status)
}

// execer is what insertWith writes through: the database, or the
// connection holding Admit's transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func (l *Ledger) insertWith(ctx context.Context, db execer, id string, c Call, tr Trace, status string) error {
	origin := c.OriginOrg
	if origin == "" {
		origin = c.OrgName
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO org_bridge_calls (id, profile_id, chain_id, hop, origin_org, direction, org_name, role_id,
		   workflow_id, execution_id, grant_id, endpoint_id, run_id, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.ProfileID, tr.ChainID, tr.Hop, origin, c.Direction, nullStr(c.OrgName), nullStr(c.RoleID),
		nullStr(c.WorkflowID), nullStr(c.ExecutionID), nullStr(c.GrantID), nullStr(c.EndpointID), nullStr(c.RunID),
		status, l.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("orgbridge: record crossing: %w", err)
	}
	return nil
}

// CountGrantCalls counts admitted calls of a grant — in one org run when
// runID is set, else since the given time.
func (l *Ledger) CountGrantCalls(ctx context.Context, grantID, runID string, since time.Time) (int, error) {
	if l.db == nil {
		return 0, nil
	}
	var n int
	var err error
	if runID != "" {
		err = l.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM org_bridge_calls WHERE grant_id = ? AND run_id = ? AND status = 'ok'`,
			grantID, runID).Scan(&n)
	} else {
		err = l.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM org_bridge_calls WHERE grant_id = ? AND status = 'ok' AND created_at >= ?`,
			grantID, since.UTC().Format(time.RFC3339Nano)).Scan(&n)
	}
	return n, err
}

// HasCrossing reports whether an execution already made a crossing of
// direction into org (org.run's exclusive check).
func (l *Ledger) HasCrossing(ctx context.Context, executionID, direction, org string) (bool, error) {
	if l.db == nil {
		return false, nil
	}
	var n int
	err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_bridge_calls WHERE execution_id = ? AND direction = ? AND org_name = ? AND status = 'ok'`,
		executionID, direction, org).Scan(&n)
	return n > 0, err
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
