# Mission tasks as host child sessions: options

> Options note, not a plan. Product question left open by the host plan and by the Missions in the host section of AGENTS.md: "a task session per mission child". Nothing here is built.

## The question

A mission today runs in the lead's turn: the conductor's workers and verifiers are in-turn processes, the board lives in the database. The host can also run **child coder sessions** (P4: depth 2, `max_children`, `max_concurrent_sessions`, spawn rate, scoped subtree tokens, a child's budget carved from its parent's, access never above the parent's, check-ins counted). Which mission tasks should become child sessions instead of in-turn workers, and how is the lead's budget split between the two?

## What is true today

- Workers die with the lead's turn; a child session has its own turn history, can be paused, stopped, resumed, and survives a lead turn that ends.
- Check-ins wake a lead with open work (follow-up 5) but a worker that stalls inside a turn is invisible to the host until the turn ends.
- A child costs a full coder turn context per task plus the lead's check-ins; a worker costs one in-turn spawn.
- Bubbles and the CLI list sessions; in-turn workers appear only as board rows and journal events of the lead.
- Tokens: a child gets a subtree token that cannot touch peers or the parent; workers use the lead's turn token and so the lead's blast radius.

## Option A: nothing becomes a session (status quo, documented)

All tasks stay in-turn. The lead budget is the one session budget.
- Cost control: simplest, one number; a runaway worker spends the lead's whole budget.
- Isolation: none beyond process; one worker's tool access is the lead's.
- Visibility: board plus lead journal only; no per-task pause or stop.
- Complexity: none. Security: nothing new to review.
- Fits: small missions (a few short tasks).

## Option B: operator-chosen tasks become children (opt-in per task)

The plan marks a task `own_session` (operator sets it at plan approval; an agent may suggest, never set). On claim the host spawns one child for that task, scoped to the mission's folder, with an explicit carve of the lead's remaining budget (task field `budget_usd`; unmarked workers share what is left, never below a floor).
- Cost control: good; each child has a hard cap the person approved, and the carve is visible in the plan.
- Isolation: strong for the marked tasks (own turns, own stop switch, own worktree if the lead has one).
- Visibility: marked tasks appear as bubbles and in `coder sessions list`, the rest as board rows.
- Complexity: medium: a new task field, a spawn-on-claim path, finishing a task from a child's result, children counted in `max_children` (a large mission hits the cap, so marking needs a ceiling).
- Security: operator-only marking keeps spawning a human decision (P4: children cannot exceed the parent's access; the carve cannot be raised by an agent).
- Fits: missions mixing a few long or risky tasks with many small ones.

## Option C: every worker task becomes a child (rule-based)

The host starts one child per claimed task automatically; the conductor only plans and verifies. The lead budget is divided evenly among open tasks, re-split when tasks finish.
- Cost control: predictable per task, but each child pays context start-up; a many-task mission costs several times option A and rebalancing logic is a new failure source.
- Isolation: best; every task stoppable on its own.
- Visibility: complete, but a 30-task mission floods bubbles and the CLI list; needs grouping under the lead.
- Complexity: high: depth/children/concurrency limits become the mission's throughput limit; queueing, partial failure and re-split need design; check-ins and the check-in streak limit multiply per child.
- Security: the most automatic spawning of unattended sessions with no human choice per spawn; every child is an unattended agent (widest blast radius, S2), so this rests entirely on the carve and subtree rules being right.
- Fits: large, long-running missions where per-task control matters more than cost.

## Recommendation

Option B, built in two steps: first the explicit carve and the `own_session` marker, with a conservative ceiling (for example at most 3 children per mission and never more than half the lead's remaining budget in children), then watch real missions before any automatic rule (C). Keep A as the default: an unmarked mission behaves exactly as today. Reasons: the person keeps the spend and the spawn decisions, nothing new is automatic, and the visible cost of isolation is paid only where it is wanted.

## What the user must decide

1. Whether the person (operator) alone marks tasks, or the lead may propose marks that the person confirms at plan approval.
2. The default ceiling: children per mission and the share of the lead's budget that may go to children; whether an unspent carve returns to the lead.
3. Whether a child's result closes the task to Review by itself (as workers do) or needs the lead to submit it.
4. Whether child sessions of a mission appear as their own bubbles or grouped under the lead.
5. Whether option C is ever wanted, and if so under which hard cap on total mission cost.
6. Who pays for check-ins of a mission's children (the carve, or the lead's budget).
