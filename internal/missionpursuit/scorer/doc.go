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
// Library only: nothing calls it yet. A guard test names the only files that may import this package (today none).
package scorer
