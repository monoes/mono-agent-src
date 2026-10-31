// Pure helpers for the Agent purposes pane (monoes/mono-agent-src#1150). The
// CLI (`monoagentcli agent purposes list|set|reset`) owns the purposes, the
// rules and the validated roster; this only reads its JSON and builds the
// arguments of a save.

export const RULES = ['any', 'order', 'cheapest']
export const FALLBACKS = ['any', 'none']
export const STATUSES = ['ok', 'stale', 'failed', 'unvalidated']
export const EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max']
export const STATUS_TONE = { ok: 'ok', stale: 'warn', failed: 'bad', unvalidated: 'muted' }

export const pairKey = (p) => `${p.runtime}:${p.model}`
export const statusOf = (s) => (STATUSES.includes(s) ? s : 'unvalidated')

const pair = (p) => ({ runtime: String(p?.runtime ?? ''), model: String(p?.model ?? ''), status: statusOf(p?.status) })

// parsePurposes reads the `agent purposes list --json` document. A document
// that is not that shape is an error, never a half-rendered pane.
export function parsePurposes(doc) {
  if (!doc || typeof doc !== 'object' || !Array.isArray(doc.purposes)) {
    throw new Error('unexpected reply from agent purposes list')
  }
  const roster = (Array.isArray(doc.roster) ? doc.roster : []).filter(p => p && p.runtime && p.model).map(pair)
  const purposes = doc.purposes.filter(p => p && p.id).map(p => ({
    id: String(p.id),
    title: String(p.title || p.id),
    category: String(p.category || ''),
    description: String(p.description || ''),
    rule: RULES.includes(p.rule) ? p.rule : 'any',
    pairs: (Array.isArray(p.pairs) ? p.pairs : []).filter(x => x && x.runtime && x.model).map(pair),
    effort: String(p.effort || ''),
    differentFromAuthor: !!p.differentFromAuthor,
    fallback: FALLBACKS.includes(p.fallback) ? p.fallback : 'any',
    budgetSharePct: Number(p.budgetSharePct) > 0 ? Number(p.budgetSharePct) : 0,
    isDefault: !!p.isDefault,
  }))
  return { purposes, roster }
}

// rosterStatus is the status of a pair as the roster sees it. A pair the
// roster no longer lists is unvalidated.
export function rosterStatus(roster, p) {
  const hit = roster.find(r => r.runtime === p.runtime && r.model === p.model)
  return hit ? hit.status : 'unvalidated'
}

// groupRoster groups the roster by system (runtime), keeping its order.
export function groupRoster(roster) {
  const groups = []
  for (const r of roster) {
    let g = groups.find(x => x.runtime === r.runtime)
    if (!g) { g = { runtime: r.runtime, models: [] }; groups.push(g) }
    g.models.push(r)
  }
  return groups
}

export function draftFrom(p) {
  return {
    pairs: p.pairs.map(x => ({ runtime: x.runtime, model: x.model })),
    rule: p.rule,
    effort: p.effort,
    differentFromAuthor: p.differentFromAuthor,
    fallback: p.fallback,
    budgetShare: p.budgetSharePct > 0 ? String(p.budgetSharePct) : '',
  }
}

export const signature = (d) => JSON.stringify([d.pairs.map(pairKey), d.rule, d.effort, d.differentFromAuthor, d.fallback, String(d.budgetShare).trim()])
export const isDirty = (draft, saved) => signature(draft) !== signature(draftFrom(saved))

// Plain decimals only: Number() would also take '1e2', '0x10' and ' 5 '.
const PLAIN_DECIMAL = /^(\d+\.?\d*|\.\d+)$/
// budgetError is '' when the share is empty (none) or a number from 0 to 100.
export function budgetError(text) {
  const s = String(text ?? '').trim()
  if (s === '') return ''
  if (!PLAIN_DECIMAL.test(s)) return 'number'
  const n = Number(s)
  return n >= 0 && n <= 100 ? '' : 'range'
}

// setArgs: the arguments of api.agentPurposeSet for a draft.
export function setArgs(id, d) {
  const share = String(d.budgetShare).trim()
  return [id, d.pairs.map(pairKey), d.rule, d.effort, d.differentFromAuthor, d.fallback, share === '' ? 0 : Number(share)]
}

// effortOptions: the known levels, plus the saved one when it is unknown.
export const effortOptions = (current) => (current && !EFFORTS.includes(current) ? [...EFFORTS, current] : EFFORTS)
