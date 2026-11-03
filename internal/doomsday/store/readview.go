package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// The read view is what the control room reads: one verified, redacted, bounded snapshot of a run. It never writes:
// every call runs on a connection with query_only on, inside one read transaction, so the run row, the chain, the
// members and the counters it reports are of one moment even while the run writes.

// Bounds of the view. Work that is not bounded by the run's size is bounded here.
const (
	DefaultFeedLimit  = 50
	MaxFeedLimit      = 200
	MaxMemberClusters = 64
	maxCounterKinds   = 32
)

var (
	// ErrNoRun: the profile has no run (or not the generation asked for).
	ErrNoRun = errors.New("doomsday: no such run")
	// ErrViewLimit: the feed limit or the page cursor is out of range. An unbounded read is refused, not clamped.
	ErrViewLimit = errors.New("doomsday: feed limit must be 1..200 and the cursor not negative")
	// ErrViewTimeout: the read did not finish inside its time budget. Nothing is reported from a read that did not finish.
	ErrViewTimeout = errors.New("doomsday: the read view did not finish in time")
)

// readViewBudget bounds one ReadView (the chain walk is O(rows)); a test replaces it.
var readViewBudget = 10 * time.Second

// Reasons of ChainState.Reason (closed set).
const (
	ReasonDoesNotVerify  = "does_not_verify"
	ReasonKeyUnavailable = "key_unavailable"
	// ReasonBehindWitness: the chain ends before the head the witness recorded (a cut tail).
	ReasonBehindWitness = "behind_witness"
	// ReasonWitnessMismatch: the chain and the witness disagree about the head (rows past it, or another MAC).
	ReasonWitnessMismatch = "witness_mismatch"
	// ReasonWitnessAhead: the witness records a generation newer than the newest the database holds (its run, charter,
	// audit and member rows were removed), or any generation when the database holds no run at all.
	ReasonWitnessAhead = "witness_ahead_generation"
)

// ViewQuery selects the page of the feed. FeedBefore 0 is the newest page; otherwise rows with seq < FeedBefore.
// FeedLimit 0 is DefaultFeedLimit; above MaxFeedLimit or negative is ErrViewLimit.
type ViewQuery struct {
	FeedBefore int64
	FeedLimit  int
}

// ChainState says whether the audit chain of the run verified. Reason is from a closed set, never an error text.
// Verified means the chain is internally consistent: it starts at the run's run.enter row, every row's MAC and link
// is valid and the sequence has no gap. That alone does not show that no tail rows were cut (the run row's MAC does
// not bind the head): Anchored says an independent record (the witness) was compared with the head and agrees. The
// store itself never sets Anchored; the layer that holds the witness does.
type ChainState struct {
	Verified bool
	Anchored bool
	Reason   string // "", a Reason* constant; with Verified and not Anchored, why there was no anchor (set by that layer)
	HeadSeq  int64
	HeadMAC  []byte
}

// MemberCluster counts the members of one kind.
type MemberCluster struct {
	Kind  string
	Total int64
	Live  int64
}

// MemberSummary is the membership clustered in SQL: totals plus at most MaxMemberClusters kinds, largest first.
type MemberSummary struct {
	Total     int64
	Live      int64
	Clusters  []MemberCluster
	Truncated bool
}

// CounterTotal is the cumulative total of one counter kind.
type CounterTotal struct {
	Kind  string
	Total int64
}

// View is a snapshot of one run. When Chain.Verified is false only Run, Limits and Chain are filled: nothing derived
// from a store that does not verify is reported.
type View struct {
	Run    Run
	Limits Limits
	// RunSpendCapMicro and RunTasksCap are the charter's caps on the run's auto-approved totals (0: not declared).
	RunSpendCapMicro int64
	RunTasksCap      int64
	Chain            ChainState
	// Feed is newest first; FeedNext is the cursor of the next older page (0: no older rows).
	Feed     []FeedItem
	FeedNext int64
	Members  MemberSummary
	Totals   []CounterTotal
}

// ReadViewer is separate from Store so existing fakes keep compiling; SQLStore satisfies it.
type ReadViewer interface {
	// ReadView reads generation (0: the profile's latest) of profile. A run or charter row that does not verify is
	// ErrUnverifiable; a chain that does not verify is reported in View.Chain.
	ReadView(ctx context.Context, profile string, generation int64, q ViewQuery) (View, error)
}

var _ ReadViewer = (*SQLStore)(nil)

// readOnlySeams are the points a test can fail; production leaves them nil.
type readOnlySeams struct {
	beforeQueryOnly func()
	afterQueryOnly  func()
	resetQueryOnly  func(ctx context.Context, conn *sql.Conn) error
}

var testReadOnlySeams readOnlySeams

// withReadOnly runs fn in a read transaction on a connection with query_only on. The connection is reset on every
// way out, a panic of fn included; if it cannot be switched off again it is discarded rather than returned to the pool.
func (s *SQLStore) withReadOnly(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var tx *sql.Tx
	defer func() { // also runs while fn panics: the panic continues after the connection is made safe
		if tx != nil {
			_ = tx.Rollback() // a read transaction has nothing to commit
		}
		reset := func(ctx context.Context, c *sql.Conn) error {
			_, e := c.ExecContext(ctx, "PRAGMA query_only = OFF")
			return e
		}
		if testReadOnlySeams.resetQueryOnly != nil {
			reset = testReadOnlySeams.resetQueryOnly
		}
		if rerr := reset(context.Background(), conn); rerr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if testReadOnlySeams.beforeQueryOnly != nil {
		testReadOnlySeams.beforeQueryOnly()
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return err
	}
	if testReadOnlySeams.afterQueryOnly != nil {
		testReadOnlySeams.afterQueryOnly()
	}
	if tx, err = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}); err != nil {
		return err
	}
	return fn(tx)
}

// ReadView implements ReadViewer.
func (s *SQLStore) ReadView(ctx context.Context, profile string, generation int64, q ViewQuery) (View, error) {
	limit := q.FeedLimit
	if limit == 0 {
		limit = DefaultFeedLimit
	}
	if limit < 0 || limit > MaxFeedLimit || q.FeedBefore < 0 || generation < 0 {
		return View{}, ErrViewLimit
	}
	var v View
	rctx, cancel := context.WithTimeout(ctx, readViewBudget)
	defer cancel()
	err := s.withReadOnly(rctx, func(tx *sql.Tx) error {
		var err error
		v, err = s.readViewTx(rctx, tx, profile, generation, q.FeedBefore, limit)
		return err
	})
	if err != nil && ctx.Err() == nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
		return View{}, fmt.Errorf("%w (budget %s)", ErrViewTimeout, readViewBudget)
	}
	return v, err
}

// generationsPast says whether the charter or audit tables hold a generation above runGen: a deleted run row leaves them behind.
func generationsPast(ctx context.Context, q querier, profile string, runGen int64) (bool, error) {
	var other int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(g),0) FROM (SELECT MAX(generation) AS g FROM doomsday_charters WHERE profile = ?
UNION ALL SELECT MAX(generation) FROM doomsday_audit WHERE profile = ?)`, profile, profile).Scan(&other)
	return other > runGen, err
}

func (s *SQLStore) readViewTx(ctx context.Context, tx *sql.Tx, profile string, generation, before int64, limit int) (View, error) {
	if generation == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM doomsday_runs WHERE profile = ?`, profile).Scan(&generation); err != nil {
			return View{}, err
		}
		past, err := generationsPast(ctx, tx, profile, generation)
		if err != nil {
			return View{}, err
		}
		if past { // a newer generation has a charter or audit rows but no run row: the run row was removed
			return View{Chain: ChainState{Reason: ReasonDoesNotVerify}}, nil
		}
		if generation == 0 {
			return View{}, ErrNoRun
		}
	}
	run, err := getRun(ctx, tx, s, profile, generation)
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		if qerr := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM doomsday_charters WHERE profile = ? AND generation = ?) + (SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ?)`,
			profile, generation, profile, generation).Scan(&n); qerr != nil {
			return View{}, qerr
		} else if n > 0 { // rows of a generation whose run row is gone
			return View{Chain: ChainState{Reason: ReasonDoesNotVerify}}, nil
		}
		return View{}, ErrNoRun
	}
	if err != nil {
		return View{}, err
	}
	ch, err := loadCharterRow(ctx, tx, s, profile, generation)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return View{}, ErrUnverifiable
		}
		return View{}, err
	}
	v := View{Run: run, Limits: ch.Limits, RunSpendCapMicro: ch.PerAsk.RunSpendMicro, RunTasksCap: ch.PerAsk.RunTasks}
	var window []FeedItem
	if v.Chain, window, err = s.verifyWindow(ctx, tx, profile, generation, before, limit); err != nil {
		return View{}, err
	}
	if !v.Chain.Verified {
		return v, nil
	}
	v.Feed = window
	if n := len(window); n > 0 && window[n-1].Seq > 1 {
		v.FeedNext = window[n-1].Seq
	}
	if v.Members, err = readMembers(ctx, tx, profile, generation); err != nil {
		return View{}, err
	}
	if v.Totals, err = readTotals(ctx, tx, profile, generation); err != nil {
		return View{}, err
	}
	return v, nil
}

// verifyWindow walks the whole chain from seq 1 (contiguous seq, prev equal to the previous MAC, valid MAC) and keeps
// only the redacted rows of the requested page, newest first. The walk is the cost of "never show a row from a chain
// that does not verify"; what it keeps is bounded by limit. A bad chain returns Verified false and no rows.
func (s *SQLStore) verifyWindow(ctx context.Context, q querier, profile string, generation, before int64, limit int) (ChainState, []FeedItem, error) {
	var head int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM doomsday_audit WHERE profile = ? AND generation = ?`, profile, generation).Scan(&head); err != nil {
		return ChainState{}, nil, err
	}
	hi := head
	if before > 0 && before-1 < hi {
		hi = before - 1
	}
	lo := hi - int64(limit) + 1
	if lo < 1 {
		lo = 1
	}
	rows, err := q.QueryContext(ctx, `SELECT seq, kind, payload, prev, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq`, profile, generation)
	if err != nil {
		return ChainState{}, nil, err
	}
	defer rows.Close()
	bad := func(reason string) (ChainState, []FeedItem, error) {
		return ChainState{Reason: reason}, nil, nil
	}
	want := int64(1)
	var prev []byte
	var window []FeedItem
	for rows.Next() {
		var seq int64
		var kind, payload string
		var p, mc []byte
		if err := rows.Scan(&seq, &kind, &payload, &p, &mc); err != nil {
			return ChainState{}, nil, err
		}
		if seq != want || !bytes.Equal(p, nonNil(prev)) || (seq == 1 && kind != "run.enter") {
			return bad(ReasonDoesNotVerify)
		}
		exp, err := s.auditMAC(profile, generation, seq, prev, kind, payload)
		if err != nil {
			return bad(ReasonKeyUnavailable)
		}
		if !hmac.Equal(exp, mc) {
			return bad(ReasonDoesNotVerify)
		}
		if seq >= lo && seq <= hi {
			window = append(window, redactRow(seq, kind, payload))
		}
		prev, want = mc, want+1
	}
	if err := rows.Err(); err != nil {
		return ChainState{}, nil, err
	}
	if want-1 != head || head < 1 { // a run always has its run.enter row: an empty chain is a chain with every row cut
		return bad(ReasonDoesNotVerify)
	}
	for i, j := 0, len(window)-1; i < j; i, j = i+1, j-1 {
		window[i], window[j] = window[j], window[i]
	}
	return ChainState{Verified: true, HeadSeq: head, HeadMAC: prev}, window, nil
}

var kindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)

func safeKind(k string) string {
	if kindPattern.MatchString(k) {
		return k
	}
	return "other"
}

// readMembers clusters in SQL: the totals are aggregates, the clusters at most MaxMemberClusters+1 rows.
func readMembers(ctx context.Context, q querier, profile string, generation int64) (MemberSummary, error) {
	var m MemberSummary
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(live),0) FROM doomsday_members WHERE profile = ? AND generation = ?`, profile, generation).Scan(&m.Total, &m.Live); err != nil {
		return MemberSummary{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT kind, COUNT(*) AS n, COALESCE(SUM(live),0) FROM doomsday_members WHERE profile = ? AND generation = ?
GROUP BY kind ORDER BY n DESC, kind LIMIT ?`, profile, generation, MaxMemberClusters+1)
	if err != nil {
		return MemberSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c MemberCluster
		if err := rows.Scan(&c.Kind, &c.Total, &c.Live); err != nil {
			return MemberSummary{}, err
		}
		if len(m.Clusters) == MaxMemberClusters {
			m.Truncated = true
			break
		}
		c.Kind = safeKind(c.Kind)
		m.Clusters = append(m.Clusters, c)
	}
	return m, rows.Err()
}

// readTotals sums the cumulative counters per kind (at most maxCounterKinds kinds; a kind that is not a plain
// identifier is folded into "other").
func readTotals(ctx context.Context, q querier, profile string, generation int64) ([]CounterTotal, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, SUM(n) FROM doomsday_counter_deltas WHERE profile = ? AND generation = ? GROUP BY kind ORDER BY kind LIMIT ?`,
		profile, generation, maxCounterKinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sums := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		sums[safeKind(k)] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CounterTotal, 0, len(sums))
	for k, n := range sums {
		out = append(out, CounterTotal{Kind: k, Total: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}
