// Package scorer scores a mission's directions from facts the host measured (task outcomes, criteria checks the host
// recorded, evidence rows, spend), so that the strategy layer's ranking never rests on what an agent wrote.
//
// It implements the shape of the epoch policy's Scorer (Score(ctx, Direction) (float64, error); the assertion is in a test
// file so that this package does not count as an importer of the doomsday strategy libraries). It reads the host through
// the tiny Facts interface and nothing else: no database, no clock, no model, no agent text. A direction's title and
// rationale are never looked at; its facts are looked up by the lead session id alone.
//
// The four criteria and the default weights are those of mission_evaluate (internal/dynorg/mission_rank.go: success,
// cost, risk, effort, weighted 3:1:1:1); a parity test keeps the arithmetic equal. Only the inputs differ: there the
// lead states the scores, here the host derives them.
//
// Fail closed: a direction with no lead session, facts that cannot be read, facts that contradict themselves (more
// passed than checked, a count beyond the bound), or no host-observed activity at all, is unscored (an error, which the
// ranking places last). An unproven direction never scores like a good one: with no finished task the risk reads as the
// worst. Bounded: one Facts call per score, counts above MaxCount are refused, the result is always in [0, 10].
//
// A direction with no criteria checked is unscored, whatever its tasks and evidence say: from one done task and five
// evidence rows alone the arithmetic gives 7.0, a number that is cheap to produce. This does not touch the parity with
// mission_evaluate: the refusal comes before the arithmetic, which stays the same.
//
// Choosing a direction: policy.Rank keeps unscored directions (at minus infinity) and still returns a first element when
// every direction is unscored. S4 MUST pick through Pick(ranked converted with Ranked(x)), which answers "none" then.
//
// Starvation (proposal for S4, not built here): a brand-new direction has no host activity and no checked criteria, so it
// is unscored and ranks behind any scored one, even a failing one; it would never get work. S4 should either give a
// new direction one host-recorded activity window before it is scored (open it, let the host record the first task and
// criteria check, rank only after), or reserve an explore slot for the best unscored direction chosen by the epoch
// order (created epoch, then id), bounded to one per epoch and charged to the away-spend limit. Pick stays strict.
//
// Requirements for the S4 adapter (R-list). The scorer trusts its Facts completely, so the adapter must supply:
//  1. TasksDone: only tasks the host itself verified as done, never a task an agent merely marked or claimed.
//  2. TasksFailed: from a real host source (task outcome rows). A source that cannot report failures must not report 0;
//     return an error instead, so the direction is unscored.
//  3. Disjoint task buckets: each task counted once, in exactly one of done, failed, open, blocked.
//  4. Criteria: counted against the WHOLE confirmed goal (all its criteria, not the ones that happen to pass) and run
//     by the host, never claimed by an agent. CriteriaChecked 0 is refused by Score itself.
//  5. The lead session id on the direction is host-written and must be checked against the mission (the session
//     belongs to this mission) before facts are read by it.
//  6. Spend: from the host ledger, as a share of the direction's budget; report SpendKnown false when there is none.
//
// Library only: nothing calls it yet. A guard test names the only files that may import this package (today none).
package scorer
