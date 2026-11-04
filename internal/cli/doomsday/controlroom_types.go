package doomsday

// The control room's JSON contract (schema_version 1). It is frozen by the golden files under testdata/ and read by
// the desktop app; a change to a field is a new schema_version, not an edit. Everything here is read-only data about
// the profile's latest run:
//
//   - `doomsday overview --json` is Overview, `doomsday feed --json [--before SEQ] [--limit N]` is FeedPage.
//   - available is false when the mode is not built in (reason "off") or the profile has no run (reason "no_run");
//     a missing database or one without the mode's tables is "off" only when the profile's witness directory holds no
//     committed or pending record; otherwise it is the read error db_unavailable (a run existed);
//     nothing else is set then, and the app shows nothing.
//     The reverse limit: deleting the witness AND the database (or the doomsday tables) reads "off" while a run may be
//     live, since nothing is left to say one existed; and a witness left behind after a manual database reset makes
//     that profile read db_unavailable until its witness directory is removed.
//   - Rows carry no timestamps (the audit stream has none), so there is no feed time and no rate: spend.rate_basis is
//     "none" and a client may estimate a rate from successive polls, labelled approximate (generated_at is the poll
//     time). The tree has no depth or parent: members are registered by kind and liveness only.
//   - integrity.chain_verified is "yes" only when the audit chain was walked from its run.enter row, every MAC and link
//     matched AND the head is CONSISTENT WITH the stop witness (an independent record: the run row does not bind the head,
//     so a chain whose tail was cut is consistent in itself). "yes" does not mean "latest": rolling back BOTH the
//     database and the witness file to an older matching pair still reads yes (a documented limit). The dbid record
//     and the OS secret store's generation anchor are not consulted by this read, and deleting the witness gives only
//     "unanchored", never "yes". The read creates and migrates nothing and never creates monoagent.db, but process
//     start may create automations/index.json, .lock and .claude_init in a fresh HOME, and the operator gate around
//     it may still touch the database file. A read that loses a race with the writer is retried once; the whole read, retry
//     included, ends within ReadTotalBudget and a slow one is read_timeout. "unanchored" is a consistent chain with no witness to
//     compare (integrity.reason says why: no_witness, no_witness_record, witness_unreadable); tree, spend and the feed
//     are shown, but the app must not show the run as healthy. "no" is a chain that does not verify, an empty chain, a
//     head behind or contradicting the witness (reasons behind_witness, witness_mismatch) or a run whose row or
//     generation is missing; "unknown" is a MAC key that was not available. Tree, spend and the feed are absent for
//     "no" and "unknown". Any value other than "yes" is not healthy.
//   - Spend counter totals are NOT authenticated (the counter rows carry no MAC): spend.counters_authenticated is
//     always false, whatever the badge says; the app labels the totals accordingly.
//   - available is false with reason "error" (and error_code: db_unavailable, read_timeout or read_failed) when the
//     read itself failed; the verb then exits non-zero and integrity is "unknown". Never "off": a run may be live.
//   - No field carries operator text, an answer, a vault name or a secret value: every string is from a closed set or a
//     short identifier (see store.FeedItem).

// ControlRoomSchema is the schema_version of every control room document.
const ControlRoomSchema = 1

// ControlRoomVerbs are the verbs of the control room, all read-only. The mode is started, approved, paused, resumed and
// granted from the terminal only; controlroom_guard_test.go fails if one of those names (or a flag of that kind)
// appears here.
var ControlRoomVerbs = []string{"overview", "feed"}

// Reasons of available:false.
const (
	ReasonOff   = "off"
	ReasonNoRun = "no_run"
	// ReasonError: the read failed (database unreadable, timeout); see Overview.ErrorCode.
	ReasonError = "error"
)

// Values of ErrorCode.
const (
	ErrCodeDBUnavailable = "db_unavailable"
	ErrCodeReadTimeout   = "read_timeout"
	ErrCodeReadFailed    = "read_failed"
)

// Values of Integrity.ChainVerified.
const (
	VerifiedYes     = "yes"
	VerifiedNo      = "no"
	VerifiedUnknown = "unknown"
	// VerifiedUnanchored: consistent chain, head not compared with the witness. Not healthy.
	VerifiedUnanchored = "unanchored"
)

// RateBasisNone is SpendInfo.RateBasis of every CLI document.
const RateBasisNone = "none"

// Overview is the document of `doomsday overview --json`.
type Overview struct {
	SchemaVersion int            `json:"schema_version"`
	Available     bool           `json:"available"`
	Reason        string         `json:"reason,omitempty"` // only when available is false
	ErrorCode     string         `json:"error_code,omitempty"`
	GeneratedAt   int64          `json:"generated_at,omitempty"`
	Run           *RunInfo       `json:"run,omitempty"`
	Tree          *TreeInfo      `json:"tree,omitempty"`
	Spend         *SpendInfo     `json:"spend,omitempty"`
	Integrity     *IntegrityInfo `json:"integrity,omitempty"`
}

// RunInfo identifies the run. State is the store's run state; Active is false only for a quiescent run, Stopping is
// true from the first Stop step on.
type RunInfo struct {
	RunID      string `json:"run_id"`
	Generation int64  `json:"generation"`
	CharterID  string `json:"charter_id"`
	State      string `json:"state"`
	Active     bool   `json:"active"`
	Stopping   bool   `json:"stopping"`
}

// TreeInfo is the membership in clusters, never one entry per member: at most store.MaxMemberClusters clusters,
// largest first; Truncated says kinds were left out. Total and Live count every member.
type TreeInfo struct {
	Total     int64     `json:"total"`
	Live      int64     `json:"live"`
	Clusters  []Cluster `json:"clusters"`
	Truncated bool      `json:"truncated"`
}

// Cluster is the members of one kind.
type Cluster struct {
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Live  int64  `json:"live"`
}

// SpendInfo is the spend meter. CapMicro is the charter's budget ceiling (null with no_ceiling or no budget);
// UsedMicro is the run's auto-approved ask spend plus granted spend. RateMicroPerHour is null from the CLI.
type SpendInfo struct {
	CapMicro         *int64   `json:"cap_micro"`
	NoCeiling        bool     `json:"no_ceiling"`
	UsedMicro        int64    `json:"used_micro"`
	AskMicro         int64    `json:"ask_micro"`
	GrantMicro       int64    `json:"grant_micro"`
	RunSpendCapMicro *int64   `json:"run_spend_cap_micro"`
	RateMicroPerHour *float64 `json:"rate_micro_per_hour"`
	RateBasis        string   `json:"rate_basis"`
	// CountersAuthenticated is always false: the counter rows carry no MAC, so the totals are unauthenticated.
	CountersAuthenticated bool      `json:"counters_authenticated"`
	Counters              []Counter `json:"counters"`
}

// Counter is one cumulative counter (the store's kinds: ask_spend_micro, ask_tasks, grant_spend_micro, sends,
// local_count, local_bytes, vault_attempts, approvals). Limit is the charter's cap where it declares one.
type Counter struct {
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Limit *int64 `json:"limit"`
}

// IntegrityInfo is the badge. ChainVerified is "yes" only when the whole audit chain was walked, every MAC matched
// and the head is consistent with the witness (not "latest"); HeadPrefix is the first 16 hex digits of the head MAC (the same as `status`).
type IntegrityInfo struct {
	ChainVerified string `json:"chain_verified"`
	Reason        string `json:"reason,omitempty"`
	HeadSeq       int64  `json:"head_seq"`
	HeadPrefix    string `json:"head_prefix,omitempty"`
}

// FeedPage is the document of `doomsday feed --json`: the decision feed, newest first. NextBefore is the cursor for
// the next older page (`--before`), 0 when there is none. Items is empty, and Integrity says why, for a chain that
// does not verify.
type FeedPage struct {
	SchemaVersion int            `json:"schema_version"`
	Available     bool           `json:"available"`
	Reason        string         `json:"reason,omitempty"`
	ErrorCode     string         `json:"error_code,omitempty"`
	GeneratedAt   int64          `json:"generated_at,omitempty"`
	Integrity     *IntegrityInfo `json:"integrity,omitempty"`
	Items         []FeedRow      `json:"items"`
	NextBefore    int64          `json:"next_before"`
}

// FeedRow is one decision row. Class is one of run, approval, plan, ask_answer, grant, spend, park, finish, refusal,
// other. Op is the typed operation (plan.approve, ask.answer, spend.grant, finish.begin) or, for class spend, the
// counter kind. Result is approved/parked/... for a decision and the new run state for a run.advance row. Kind is the
// audit kind or "(redacted)". Ref is an opaque approval id.
type FeedRow struct {
	Seq         int64    `json:"seq"`
	Kind        string   `json:"kind"`
	Class       string   `json:"class"`
	Op          string   `json:"op,omitempty"`
	Result      string   `json:"result,omitempty"`
	AmountMicro *int64   `json:"amount_micro,omitempty"`
	Count       *int64   `json:"count,omitempty"`
	AnswerClass string   `json:"answer_class,omitempty"`
	Exceeded    []string `json:"exceeded,omitempty"`
	Ref         string   `json:"ref,omitempty"`
}
