// What to do about a failed turn, keyed by the code turn.finished carries. The CLI says what failed (reason, code);
// this table only adds the next step for the codes that have one. An unknown code gets no hint: the raw reason stands.
const HINTS = {
  agent_not_setup: 'The AI agent is not installed or not signed in. Open AI agents, set one up, then send again.',
  auth: 'The AI agent rejected its sign-in. Sign in again on the AI agents page, then send again.',
  quota: 'The AI provider says the quota is used up. Wait for it to reset or switch to another agent or model, then send again.',
  'rate-limited': 'The AI provider is rate limiting. Wait a minute, or switch to another model, then send again.',
  'missing-binary': 'The AI agent program was not found. Check AI agents for its install state, then send again.',
  budget: 'The turn hit its spending limit. Raise the limit or ask for less, then send again.',
  timeout: 'The turn ran out of time. Ask for a smaller step, then send again.',
}

/** The next step for a failure code, or '' when the code has none. */
export function failureHint(code) {
  return (code && HINTS[code]) || ''
}
