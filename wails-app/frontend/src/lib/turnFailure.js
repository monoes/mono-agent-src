import i18nOr from './i18nOr.js'

// What to do about a failed turn, keyed by the code turn.finished carries. The CLI says what failed (reason, code);
// this table only adds the next step for the codes that have one. An unknown code gets no hint: the raw reason stands.
const HINTS = {
  agent_not_setup: 'ui6Node.turnAgentSetup',
  auth: 'ui6Node.turnAuth',
  quota: 'ui6Node.turnQuota',
  'rate-limited': 'ui6Node.turnRate',
  'missing-binary': 'ui6Node.turnMissing',
  budget: 'ui6Node.turnBudget',
  timeout: 'ui6Node.turnTimeout',
}

/** The next step for a failure code, or '' when the code has none. */
export function failureHint(code) {
  return typeof code === 'string' && Object.hasOwn(HINTS, code) ? i18nOr(HINTS[code]) : ''
}

const MAX_REASON = 500
// Bidi controls and C0/C1 control characters (and DEL) never belong in a reason.
const UNSAFE = /[\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]+/g

/** A reason as shown: control and bidi characters gone, one line, cut to a sane length. */
export function cleanReason(text) {
  if (typeof text !== 'string') return ''
  const t = text.replace(UNSAFE, ' ').replace(/\s+/g, ' ').trim()
  return t.length > MAX_REASON ? `${t.slice(0, MAX_REASON)}…` : t
}
