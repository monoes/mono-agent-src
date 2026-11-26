// Display helpers for `monoagentcli org budget` (internal/monomind
// SectionBudgets). All numbers and states come from the CLI; nothing here
// recomputes spend, so the UI cannot disagree with `org report`.
import i18n from '../../i18n.js'

export const BUDGET_COLORS = {
  ok: 'var(--green-neon)',
  warn: '#eab308',
  closed: 'var(--red)',
  unallocated: 'var(--text-muted)',
}

export const usd = (n) => (typeof n === 'number' ? `$${n.toFixed(2)}` : '—')

// {name: ScopeBudget} for the sections of a report; {} for an error or nothing.
export function bySection(report) {
  if (!report || report.error || !Array.isArray(report.sections)) return {}
  return Object.fromEntries(report.sections.map(s => [s.name, s]))
}

// One short line for a scope. A scope with no allocation shows only spend.
export function budgetLine(b) {
  if (!b) return ''
  if (b.allocation_usd == null) return i18n.t('ui6OrgsCanvas.spent_only', { amount: usd(b.spent_usd) })
  return `${usd(b.spent_usd)} / ${usd(b.allocation_usd)}`
}

// The state to show: monomind's soft-closure (from its own audit event) wins
// over the spend-derived state; a section at its allocation with no closure
// event is still "closed" by spend but not soft-closed.
export function budgetBadge(b) {
  if (!b) return null
  if (b.soft_closed) return { key: 'soft-closed', label: i18n.t('ui6OrgsCanvas.badge_soft_closed'), color: BUDGET_COLORS.closed }
  if (b.state === 'closed') return { key: 'over', label: i18n.t('ui6OrgsCanvas.badge_at_allocation'), color: BUDGET_COLORS.closed }
  if (b.state === 'warn') return { key: 'warn', label: i18n.t('ui6OrgsCanvas.badge_left', { amount: usd(b.remaining_usd) }), color: BUDGET_COLORS.warn }
  return null
}

export function budgetTitle(b) {
  if (!b) return ''
  const parts = [`${budgetLine(b)}${b.fraction != null ? ` (${Math.round(b.fraction * 100)}%)` : ''}`]
  if (b.role_cap_sum_usd) parts.push(i18n.t('ui6OrgsCanvas.role_caps_total', { amount: usd(b.role_cap_sum_usd) }))
  if (b.soft_closed) {
    parts.push(i18n.t('ui6Orgs.soft_closed_at', { time: new Date(b.closed_at).toLocaleTimeString() }))
    if (b.closed_roles?.length) parts.push(i18n.t('ui6OrgsCanvas.closed_list', { roles: b.closed_roles.join(', ') }))
    if (b.held_tasks?.length) parts.push(i18n.t('ui6OrgsCanvas.held_tasks', { tasks: b.held_tasks.join(', ') }))
  } else if (b.state === 'warn') parts.push(i18n.t('ui6OrgsCanvas.past_80'))
  if (b.cost_unknown) parts.push(i18n.t('ui6OrgsCanvas.cost_unknown_bound'))
  return parts.join(' · ')
}

// {roleId: RoleBudget} across sections and the root reserve, only roles with a cap.
export function roleBudgets(report) {
  if (!report || report.error) return {}
  const scopes = [...(report.sections || []), ...(report.reserve ? [report.reserve] : [])]
  const out = {}
  for (const sc of scopes) for (const r of sc.roles || []) if (r.cap_usd != null) out[r.id] = r
  return out
}

// Badge for a role node: spend against its own cap; warn from 80%.
export function roleBadge(r) {
  if (!r || r.cap_usd == null) return null
  const color = BUDGET_COLORS[r.state] || BUDGET_COLORS.ok
  const pct = r.fraction != null ? ` (${Math.round(r.fraction * 100)}%)` : ''
  const note = r.state === 'closed' ? i18n.t('ui6OrgsCanvas.at_cap') : r.state === 'warn' ? i18n.t('ui6OrgsCanvas.near_cap') : ''
  return {
    text: `${usd(r.spent_usd)}/${usd(r.cap_usd)}`, color, state: r.state,
    title: `${i18n.t('ui6OrgsCanvas.role_cap_title', { id: r.id, spent: usd(r.spent_usd), cap: usd(r.cap_usd) })}${pct}${note}${r.cost_unknown ? i18n.t('ui6OrgsCanvas.cost_unknown_short') : ''}`,
  }
}
