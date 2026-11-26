// Org status panel: per-section spend against allocation, role caps and
// soft-closure state for one run. Presentational over the `org budget`
// report; renders nothing for an org with no sections or no allocation.
import React from 'react'
import { Chip } from './ui.jsx'
import { sectionLabel, mutedText } from './ui.jsx'
import useOrgBudget from '../orgdesigner/useOrgBudget.js'
import { BUDGET_COLORS, budgetLine, budgetBadge, budgetTitle, usd } from '../orgdesigner/budgetModel.js'
import { useTranslation } from 'react-i18next'

function Bar({ b }) {
  const f = b.fraction == null ? 0 : Math.min(1, b.fraction)
  return (
    <div role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(f * 100)}
      style={{ height: 4, borderRadius: 2, background: 'var(--border)', overflow: 'hidden' }}>
      <div style={{ width: `${f * 100}%`, height: '100%', background: BUDGET_COLORS[b.state] || BUDGET_COLORS.ok }} />
    </div>
  )
}

function Row({ title, b, children }) {
  const badge = budgetBadge(b)
  return (
    <div data-testid={`budget-${b.kind}-${b.name || b.kind}`} title={budgetTitle(b)}
      style={{ display: 'flex', flexDirection: 'column', gap: 3, padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius)', background: 'var(--surface)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontFamily: 'var(--font-mono)', fontSize: 11 }}>
        <strong>{title}</strong>
        <span style={{ color: 'var(--text-secondary)' }}>{budgetLine(b)}</span>
        {badge && <Chip color={badge.color} style={{ marginLeft: 'auto' }}><span data-testid="budget-badge">{badge.label}</span></Chip>}
      </div>
      {b.allocation_usd != null && <Bar b={b} />}
      {children}
    </div>
  )
}

export function SectionBudgetView({ report, estimate }) {
  const { t } = useTranslation()
  if (!report || report.error || !report.sections?.length) return null
  const allocated = report.sections.some(s => s.allocation_usd != null) || report.total.allocation_usd != null
  if (!allocated) return null
  const totalBadge = budgetBadge(report.total)
  return (
    <div data-testid="section-budget-panel" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={sectionLabel}>{t('ui6Orgs.section_budgets')}</div>
      {report.sections.map(s => (
        <Row key={s.name} title={s.name} b={s}>
          {s.roles?.some(r => r.cap_usd != null) && (
            <div style={{ ...mutedText, display: 'flex', flexWrap: 'wrap', gap: '2px 10px' }}>
              {s.roles.filter(r => r.cap_usd != null).map(r => (
                <span key={r.id} data-testid={`role-cap-${r.id}`} style={{ color: r.state === 'closed' ? BUDGET_COLORS.closed : r.state === 'warn' ? BUDGET_COLORS.warn : undefined }}>
                  {r.id} {usd(r.spent_usd)}/{usd(r.cap_usd)}
                </span>
              ))}
            </div>
          )}
          {s.soft_closed && (
            <div data-testid="soft-closure" style={mutedText}>
              {t('ui6Orgs.soft_closed_at', { time: new Date(s.closed_at).toLocaleTimeString() })}
              {s.closed_roles?.length ? t('ui6Orgs.closed_roles', { roles: s.closed_roles.join(', ') }) : ''}
              {s.held_tasks?.length ? t('ui6Orgs.tasks_held', { total: s.held_tasks.length }) : ''}
            </div>
          )}
        </Row>
      ))}
      {report.reserve?.allocation_usd != null && <Row title={t('ui6Orgs.root_reserve')} b={report.reserve} />}
      {estimate?.stale_rates && (
        <div data-testid="stale-rates" title={estimate.text} style={{ ...mutedText, color: '#eab308' }}>{estimate.stale_rates}</div>
      )}
      <div data-testid="budget-total" style={{ ...mutedText, display: 'flex', gap: 8, alignItems: 'center' }}>
        <span>{t('ui6Orgs.org_total', { line: budgetLine(report.total) })}</span>
        <span>{t('ui6Orgs.total_tokens', { total: report.total_tokens })}</span>
        {totalBadge && <Chip color={totalBadge.color}>{totalBadge.label}</Chip>}
        {!report.cost_complete && <Chip color="#eab308" title={t('ui6Orgs.cost_incomplete_title')}>{t('ui6Orgs.cost_incomplete')}</Chip>}
      </div>
    </div>
  )
}

// Fetches the report for a run ('' = the current one) and follows a live run.
export default function SectionBudgetPanel({ orgName, run = '', live = false }) {
  const { report, estimate } = useOrgBudget({ orgName, enabled: true, run, live })
  return <SectionBudgetView report={report} estimate={estimate} />
}
