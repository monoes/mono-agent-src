// Pure helpers behind the Help page's buttons. No React, no globals: the
// caller passes App's navigate and the coder bubble store's openNewChat.

// Page ids App.jsx can route to (its persistentPages keys, minus the
// conditional controlroom; detail pages need an id so are not link targets).
// helpActions.test.js fails if App.jsx drifts from this list.
export const KNOWN_PAGES = Object.freeze([
  'dashboard', 'noderunner', 'people', 'communications', 'publication',
  'connections', 'vault', 'secretsVault', 'applications', 'documents',
  'info', 'tasks', 'ai', 'orgs', 'logs', 'settings',
])

export const MAX_PROMPT_LENGTH = 4000

const SECRET_PATTERNS = [
  /\bsk-[A-Za-z0-9_-]{16,}/g,
  /\bgh[pousr]_[A-Za-z0-9]{20,}/g,
  /\bAKIA[0-9A-Z]{16}\b/g,
  /\bBearer\s+[A-Za-z0-9._~+/=-]{16,}/gi,
  /\b(api[_-]?key|token|secret|password)\s*[:=]\s*\S+/gi,
]

function sanitizePrompt(text) {
  let out = String(text).trim()
  for (const re of SECRET_PATTERNS) out = out.replace(re, '[redacted]')
  if (out.length > MAX_PROMPT_LENGTH) out = out.slice(0, MAX_PROMPT_LENGTH).trimEnd()
  return out
}

const fail = (reason) => ({ ok: false, reason })

export function runHelpAction(action, deps) {
  if (!action || typeof action !== 'object') return fail('invalid action')
  const { navigate, openNewChat } = deps || {}
  if (action.kind === 'goto') {
    if (!KNOWN_PAGES.includes(action.target)) return fail(`unknown page: ${String(action.target)}`)
    if (typeof navigate !== 'function') return fail('navigation unavailable')
    if (action.params && (typeof action.params !== 'object' || Array.isArray(action.params))) return fail('invalid params')
    // A fresh object each time so pages keyed on navData identity re-fire.
    navigate(action.target, action.params ? { ...action.params } : undefined)
    return { ok: true }
  }
  if (action.kind === 'coder') {
    const prompt = typeof action.prompt === 'string' ? sanitizePrompt(action.prompt) : ''
    if (!prompt) return fail('empty prompt')
    if (typeof openNewChat !== 'function') return fail('coder chat unavailable')
    openNewChat(action.setup ? { prompt, setup: action.setup } : { prompt })
    return { ok: true }
  }
  return fail(`unknown action kind: ${String(action.kind)}`)
}

const PAGE_LABELS = {
  noderunner: 'Workflows', secretsVault: 'Secrets vault', vault: 'Media vault',
  ai: 'AI agents', orgs: 'Orgs', info: 'Info', logs: 'Logs',
}

export function describeAction(action) {
  if (action?.kind === 'goto') {
    const t = String(action.target ?? '')
    const label = PAGE_LABELS[t] || (t ? t[0].toUpperCase() + t.slice(1) : 'page')
    return `Open ${label}`
  }
  if (action?.kind === 'coder') return 'Ask the coder'
  return 'Open'
}
