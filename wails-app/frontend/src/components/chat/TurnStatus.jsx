import { useEffect, useState } from 'react'
import { Loader, Square } from 'lucide-react'
import { failureHint, cleanReason } from '../../lib/turnFailure.js'
import { useTranslation } from 'react-i18next'
import i18nOr from '../../lib/i18nOr.js'

// Idle-activity threshold (plan §"Honest progress"): "After 15 seconds
// without activity, show 'No new activity for 15s'; silence is not proof
// of a hang."
const IDLE_WARNING_MS = 15000

const TERMINAL_KEYS = {
  completed: 'ui7Chat.turn_completed',
  failed: 'ui7Chat.turn_failed',
  cancelled: 'ui7Chat.turn_stopped',
  interrupted: 'ui7Chat.turn_interrupted',
}

// computeStatusLabel derives one of the plan's exact honest-progress
// strings from reducer state — Starting agent, Waiting for response,
// Running <tool>, Responding, Stopping, Completed, Failed, Stopped — never
// a fabricated percentage or invented reasoning. Pure and independent of
// wall-clock timers so it is directly unit-testable; `now` (epoch ms) is a
// parameter rather than Date.now() for the same reason. Returns
// { label, isIdleWarning } — the component below re-renders this on a
// ticking clock to keep isIdleWarning current.
//
// streamsIncrementally (agent-exec.ts protocol rev 5 — see
// runner-registry.ts's RunnerSpec.streamsIncrementally in the monomind
// repo): most local agent runtimes only ever deliver a complete response
// at a step/turn boundary, never partial text as it generates. Silently
// waiting through that with the generic "Waiting for response" label and
// the "No new activity for Ns" idle warning would tell the user something
// might be stuck when nothing is wrong — that turn was never going to show
// partial output. Defaults to `true` (today's behavior, honest for a
// streaming-capable runtime) so old data, mocks, and any caller not
// passing it are unaffected — only an explicit `false` changes anything,
// same convention as ownedByThisInstance below.
export function computeStatusLabel(state, now) {
  const { terminal, stopRequested, startedAt, lastEventAt, parts, calls, ownedByThisInstance, streamsIncrementally = true } = state
  if (terminal) {
    let label = Object.hasOwn(TERMINAL_KEYS, terminal.status) ? i18nOr(TERMINAL_KEYS[terminal.status]) : terminal.status
    if (terminal.status === 'completed' && terminal.reason && terminal.reason.startsWith('limit reached')) {
      label = i18nOr('ui7Chat.turn_limit')
    }
    return { label, isIdleWarning: false }
  }
  // Cross-instance ownership (GetChatTurns' ownedByThisInstance — see
  // docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md,
  // "OwnerInstanceID is written and read back but never compared to
  // anything"): an active turn genuinely owned by a DIFFERENT live app
  // instance sharing this database is real and ongoing, but this instance
  // has no event stream for it — a spinner or ticking elapsed/idle time
  // here would imply liveness we cannot actually observe (plan §"Honest
  // progress": "Present observed activity ... Do not invent hidden
  // reasoning ... or successful results"). The terminal check just above
  // always wins over this one: if the event log already proves the turn
  // finished, that is more concrete truth than a turn-list snapshot taken
  // moments earlier. ownedByThisInstance is only ever `false` when
  // GetChatTurns says so explicitly — absent/undefined (older data, a
  // mock, or the backend half of this contract not deployed yet)
  // defensively falls through to normal handling below.
  if (ownedByThisInstance === false) return { label: i18nOr('ui7Chat.turn_other_window'), isIdleWarning: false }
  if (stopRequested) return { label: i18nOr('ui7Chat.turn_stopping'), isIdleWarning: false }

  let label
  const runningTool = Object.values(calls || {}).find(c => c.status === 'started')
  if (runningTool) label = i18nOr('ui7Chat.turn_running_tool', { name: runningTool.name })
  else if ((parts || []).some(p => p.kind === 'text')) label = i18nOr('ui7Chat.turn_responding')
  else if (!startedAt) label = i18nOr('ui7Chat.turn_starting')
  else if (!streamsIncrementally) {
    // No tool running, no text yet, and this runtime never sends partial
    // output — a minute of silence here is normal, not a stall. Skip the
    // idle-activity annotation entirely rather than reusing "No new
    // activity for Ns", which specifically implies something might be
    // wrong.
    return { label: i18nOr('ui7Chat.turn_waiting_full'), isIdleWarning: false }
  } else label = i18nOr('ui7Chat.turn_waiting')

  const isIdleWarning = !!lastEventAt && (now - new Date(lastEventAt).getTime()) > IDLE_WARNING_MS
  return { label, isIdleWarning }
}

// TurnStatus renders the live label in a polite ARIA status region (plan
// §Accessibility: "announce meaningful state changes in a polite status
// region, not every token") plus the idle-activity notice, ticking once a
// second only while the turn is still active.
// ownedByThisInstance (default true — see this component's own doc comment
// on `stopRequested` just below for why a value like this is threaded in as
// a separate prop rather than living inside chatReducer's state) reflects
// GetChatTurns' cross-instance-ownership field. Defaulting to true means an
// absent/undefined value (older data, a mock, or the backend half of this
// contract not yet deployed) renders exactly like today — only an explicit
// `false` changes anything. streamsIncrementally (default true, same
// convention) reflects the active runtime's own capability — see
// computeStatusLabel's doc comment above.
export function TurnStatus({ state, stopRequested, ownedByThisInstance = true, streamsIncrementally = true }) {
  useTranslation() // renders again on a language change
  // "Active" is whatever chatReducer state itself considers active — no
  // terminal event yet observed for this turn.
  const isForeignActive = !state.terminal && ownedByThisInstance === false

  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    // Nothing to tick towards: a foreign-owned turn's displayed status can
    // only change by reloading the conversation (this instance has no live
    // event stream for someone else's turn) — a running clock here would
    // just churn re-renders for a label that never changes on its own.
    if (state.terminal || isForeignActive) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [state.terminal, isForeignActive])

  // computeStatusLabel reads stopRequested/ownedByThisInstance/
  // streamsIncrementally off the object it's given, not as separate
  // arguments — but chatReducer's own state shape (see initialChatState())
  // has none of these fields; the chat view tracks stopRequested as its
  // own local UI state, ownedByThisInstance comes from the raw
  // GetChatTurns turn record, and streamsIncrementally comes from the
  // active runtime's scan entry — none from event replay. Without merging
  // them in here, all three are always undefined in the real app, and
  // their respective labels could never actually appear — only the icon
  // swap below would, silently.
  const { label, isIdleWarning } = computeStatusLabel({ ...state, stopRequested, ownedByThisInstance, streamsIncrementally }, now)
  const isTerminal = !!state.terminal
  const isFailure = state.terminal && state.terminal.status !== 'completed'
  // A failed turn says why, in plain text; the reason is skipped when the turn already shows the same text as a notice
  // (a refused turn recorded in the conversation carries its reason both ways).
  const failed = state.terminal?.status === 'failed'
  const reason = failed ? cleanReason(state.terminal.reason) : ''
  const failureReason = reason && !(state.notices || []).some(n => cleanReason(n.message) === reason) ? reason : ''
  const hint = failed ? failureHint(state.terminal.code) : ''

  return (
    <div
      role="status"
      aria-live="polite"
      style={{
        display: 'flex', alignItems: 'center', gap: 6,
        padding: '4px 0',
        fontFamily: 'var(--font-mono)', fontSize: 10,
        color: isFailure ? '#fca5a5' : 'var(--text-muted)',
      }}
    >
      {!isTerminal && !isForeignActive && (stopRequested
        ? <Square size={11} color="#ef4444" />
        : <Loader size={11} className="chat-spin" style={{ color: '#00b4d8' }} />
      )}
      <span>{label}</span>
      {isIdleWarning && <span style={{ color: '#fbbf24' }}>{i18nOr('ui7Chat.turn_idle')}</span>}
      {failureReason && <span>· {failureReason}</span>}
      {hint && <span data-testid="turn-failure-hint">· {hint}</span>}
    </div>
  )
}
