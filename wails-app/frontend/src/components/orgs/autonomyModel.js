// Autonomy vocabulary and pure helpers for the org UI (plan §7.7, contracts §8).
// The CLI assigns the real tier (Go, before any decider runs); these helpers
// only preview it — e.g. in the grant dialog before a grant row exists — and
// explain where a tier is routed at each level.
import i18nOr from '../../lib/i18nOr.js'

// label/hint are the English wording; the UI shows the locale's
// orgs.autonomy.levels.<id> / <id>Hint (and deciders.*) instead.
export const LEVELS = [
  { id: 'manual', label: 'Manual', hint: 'You decide everything.' },
  { id: 'mid', label: 'Mid', hint: 'Routine by rule, consequential by the decider, irreversible by you.' },
  { id: 'full', label: 'Full auto', hint: 'Routine by rule, everything else by the decider. No human.' },
]

export const DECIDERS = [
  { id: 'model', label: 'Model', hint: 'A separate one-shot model call.' },
  { id: 'boss', label: 'Boss', hint: "The org's root role decides." },
  { id: 'parent', label: 'Parent', hint: "The holding org's initiator decides." },
  { id: 'jev', label: 'Jev', hint: 'TypeSafe Jev picks the verdict in one quick call; the model decides questions and anything Jev is unsure of.' },
]

/** The jev decider's default gate (CLI orgdecide.DefaultJevThreshold). */
export const JEV_THRESHOLD = 0.8

/**
 * Parse a jev threshold field: a number in [0.05, 1], snapped to the 0.05
 * step the field offers. Returns null for anything else.
 */
export function parseJevThreshold(raw) {
  if (raw === '' || raw == null) return null
  const n = Number(raw)
  if (!Number.isFinite(n) || n < 0.05 || n > 1) return null
  return Math.round(n * 20) / 20
}

/** Decider kinds the CLI accepts (`org autonomy set --decider`). */
export function isDeciderKind(kind) {
  return DECIDERS.some(d => d.id === kind)
}

export const TIERS = ['routine', 'consequential', 'irreversible']

export const TIER_COLORS = {
  routine: 'var(--text-muted)',
  consequential: '#eab308',
  irreversible: 'var(--red, #ef4444)',
}

// Defaults from plan §7.7; the CLI's `default_tiers` wins when present.
const BASE_DEFAULTS = {
  'tool:*': 'routine',
  org_complete: 'consequential',
  question: 'consequential',
  org_start: 'consequential',
  gate: 'irreversible',
}

function wildcardOf(cls) {
  if (cls.startsWith('tool:')) return 'tool:*'
  if (cls.startsWith('grant:')) return 'grant:*'
  if (cls.startsWith('hil:')) return 'hil:*'
  return null
}

/**
 * Preview the tier a decision class gets. Order: the org's explicit tier for
 * the exact class, its wildcard, the CLI's default_tiers, then plan defaults.
 * A grant with outbound nodes (comm.*, service writes, social) defaults to
 * irreversible, without them to consequential.
 */
export function tierForClass(cls, autonomy, { hasOutbound = false } = {}) {
  if (!cls) return 'consequential'
  const tiers = autonomy?.tiers || {}
  const defaults = autonomy?.default_tiers || {}
  const wild = wildcardOf(cls)
  if (tiers[cls]) return tiers[cls]
  if (wild && tiers[wild]) return tiers[wild]
  if (cls.startsWith('grant:')) {
    if (defaults[cls]) return defaults[cls]
    return hasOutbound ? 'irreversible' : 'consequential'
  }
  if (defaults[cls]) return defaults[cls]
  if (wild && defaults[wild]) return defaults[wild]
  if (BASE_DEFAULTS[cls]) return BASE_DEFAULTS[cls]
  if (wild && BASE_DEFAULTS[wild]) return BASE_DEFAULTS[wild]
  return 'consequential'
}

/** The level that actually routes decisions right now (pause → manual). */
export function effectiveLevel(autonomy, now = Date.now()) {
  if (!autonomy) return 'manual'
  if (autonomy.effective_level) return autonomy.effective_level
  if (isPaused(autonomy, now)) return 'manual'
  return autonomy.level || 'manual'
}

export function isPaused(autonomy, now = Date.now()) {
  const until = autonomy?.paused_until
  if (!until) return false
  const t = typeof until === 'number' ? until : Date.parse(until)
  // A far-future or unparseable timestamp still means "paused until resumed".
  return Number.isNaN(t) ? true : t > now
}

/** Who resolves a tier at a level: 'you' | 'rule' | 'decider'. */
export function routeFor(level, tier) {
  if (level === 'mid') return tier === 'routine' ? 'rule' : tier === 'consequential' ? 'decider' : 'you'
  if (level === 'full') return tier === 'routine' ? 'rule' : 'decider'
  return 'you'
}

export function routeLabel(route, deciderKind) {
  if (route === 'rule') return i18nOr('ui6Orgs.route_rule')
  if (route === 'decider') {
    const kind = deciderKind || 'model'
    // English keeps the CLI's own word; other languages name it.
    return i18nOr('ui6Orgs.route_decider', { who: i18nOr(`ui6Orgs.decider_${kind}`, { defaultValue: kind }) })
  }
  return i18nOr('ui6Orgs.route_you')
}

/**
 * What the decider will resolve with no human once the org is at full auto:
 * gates (always irreversible by default), grants whose row says
 * tier "irreversible", and any class the operator moved to irreversible.
 */
export function fullAutoImpact(autonomy, grants = [], pendingGates = []) {
  const grantItems = (grants || [])
    .filter(g => g.tier === 'irreversible')
    .map(g => ({ role: g.role, alias: g.alias, workflowName: g.workflow_name || g.alias }))
  const overrideClasses = Object.entries(autonomy?.tiers || {})
    .filter(([cls, tier]) => tier === 'irreversible' && cls !== 'gate' && !cls.startsWith('grant:'))
    .map(([cls]) => cls)
  const gateTier = tierForClass('gate', autonomy)
  return {
    gatesIncluded: gateTier === 'irreversible',
    pendingGates: (pendingGates || []).map(g => ({ id: g.id || g.gateId, name: g.name || g.id, role: g.roleId || g.role })),
    grants: grantItems,
    classes: overrideClasses,
  }
}

/** Map a monomind approval action to its decision class (contracts §8). */
export function classOfAction(action) {
  if (!action) return null
  if (action === 'org_complete') return 'org_complete'
  const bare = action.replace(/^mcp__org__/, '')
  if (bare === 'monoagent__org_start' || bare === 'org_start') return 'org_start'
  const m = /^monoagent__automation_(.+)$/.exec(bare)
  if (m) return `grant:${m[1]}`
  return `tool:${bare}`
}

/** A grant's approval control copy: "Needs a decision: yes / no". */
export function approvalToNeedsDecision(approval) {
  return approval === 'required'
}
export function needsDecisionToApproval(needs) {
  return needs ? 'required' : 'none'
}
