// Right-panel editors for sections: SectionInspector (the selected section's
// budget, writes, rework rounds, lead, role caps, document edges, delete) and
// RoleSectionControls (a selected role's section and lead mark). Both only
// report intent; OrgDesigner calls the backend and shows its refusals.

import { useEffect, useState } from 'react'
import { sectionOfRole } from './sectionsGraph.js'
import { budgetLine, budgetBadge, budgetTitle, usd } from './budgetModel.js'
import { useTranslation } from 'react-i18next'

const field = { display: 'flex', flexDirection: 'column', gap: 3, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }
const input = { fontFamily: 'var(--font-mono)', fontSize: 11, padding: '4px 6px', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 5, color: 'var(--text)' }
const selectStyle = { fontSize: 11, padding: '3px 22px 3px 6px' }
const btn = { fontFamily: 'var(--font-mono)', fontSize: 10, padding: '3px 8px', background: 'transparent', border: '1px solid var(--border)', borderRadius: 5, color: 'var(--text-secondary)', cursor: 'pointer' }

// A text input that commits on blur / Enter, and re-syncs when the saved value changes.
function Committed({ label, value, onCommit, type = 'text', placeholder, testId }) {
  const [v, setV] = useState(value)
  useEffect(() => setV(value), [value])
  const commit = () => { if (v !== value) onCommit(v) }
  return (
    <label style={field}>
      {label}
      <input
        data-testid={testId} type={type} value={v} placeholder={placeholder} style={input}
        min={type === 'number' ? 0 : undefined} step={type === 'number' ? 'any' : undefined}
        onChange={e => setV(e.target.value)} onBlur={commit}
        onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur() }}
      />
    </label>
  )
}

const num = (s) => (s === '' ? null : Number(s))

export function SectionInspector({ section, budget, nodes, sections, edges, onUpdate, onPatchRole, onRemoveEdge, onDelete }) {
  const { t } = useTranslation()
  const [moveTo, setMoveTo] = useState('')
  const others = sections.filter(s => s.name !== section.name)
  useEffect(() => setMoveTo(others[0]?.name || ''), [section.name]) // eslint-disable-line react-hooks/exhaustive-deps
  const members = section.roster.map(id => nodes.find(n => n.id === id)).filter(Boolean)
  const caps = members.reduce((sum, n) => sum + (typeof n.rest?.budget_usd === 'number' ? n.rest.budget_usd : 0), 0)
  const mine = edges.filter(e => e.from === section.name || e.to === section.name)
  return (
    <div data-testid="section-inspector" style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
      <strong style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: section.color }}>{t('ui6OrgsCanvas.section_heading', { name: section.name })}</strong>

      <label style={field}>
        {t('ui6OrgsCanvas.lead')}
        <select data-testid="section-lead" value={section.leadId} className="filter-select" style={selectStyle} onChange={e => onUpdate({ lead: e.target.value })}>
          {section.roster.map(id => <option key={id} value={id}>{id}</option>)}
        </select>
      </label>
      {budget && (
        <div data-testid="section-live-budget" title={budgetTitle(budget)} style={{ ...field, color: budgetBadge(budget)?.color || 'var(--text-secondary)' }}>
          {t('ui6OrgsCanvas.spend_this_run', { line: budgetLine(budget) })}{budgetBadge(budget) ? ` — ${budgetBadge(budget).label}` : ''}
          {budget.roles?.filter(r => r.cap_usd != null).map(r => <span key={r.id}>{t('ui6OrgsCanvas.role_spend', { id: r.id, spent: usd(r.spent_usd), cap: usd(r.cap_usd) })}</span>)}
        </div>
      )}
      <Committed testId="section-budget" label={t('ui6OrgsCanvas.budget_usd')} type="number" placeholder={t('ui6OrgsCanvas.no_section_budget')} value={section.budgetUsd ?? ''} onCommit={v => onUpdate({ budget_usd: num(v) })} />
      <Committed testId="section-rework" label={t('ui6OrgsCanvas.max_rework')} type="number" placeholder={t('ui6OrgsCanvas.runtime_default')} value={section.maxReworkRounds ?? ''} onCommit={v => onUpdate({ max_rework_rounds: num(v) })} />
      <Committed testId="section-writes" label={t('ui6OrgsCanvas.writes')} placeholder="src/**, docs/**" value={section.writes.join(', ')}
        onCommit={v => onUpdate({ writes: v.split(',').map(x => x.trim()).filter(Boolean) })} />

      <div style={field}>
        {t('ui6OrgsCanvas.role_caps')}{section.budgetUsd != null && <span>{t('ui6OrgsCanvas.caps_allocated', { caps, budget: section.budgetUsd })}</span>}
        {members.map(n => (
          <Committed key={n.id} testId={`cap-${n.id}`} label={n.id === section.leadId ? t('ui6OrgsCanvas.lead_suffix', { id: n.id }) : n.id} type="number" placeholder={t('ui6OrgsCanvas.no_cap')}
            value={n.rest?.budget_usd ?? ''} onCommit={v => v !== '' && onPatchRole(n.id, { budget_usd: Number(v) })} />
        ))}
      </div>

      <div style={field}>
        {t('ui6OrgsCanvas.document_edges')}
        {mine.length === 0 && <span>{t('ui6OrgsCanvas.no_edges')}</span>}
        {mine.map(e => (
          <div key={`${e.type}${e.from}${e.to}`} style={{ display: 'flex', alignItems: 'center', gap: 6, color: 'var(--text-secondary)' }}>
            <span style={{ flex: 1 }}>{e.from} → {e.to} <em>{e.type}</em></span>
            <button style={btn} aria-label={t('ui6OrgsCanvas.remove_edge', { type: e.type })} onClick={() => onRemoveEdge(e)}>{t('ui6OrgsCanvas.remove')}</button>
          </div>
        ))}
      </div>

      <div style={{ ...field, borderTop: '1px solid var(--border)', paddingTop: 8 }}>
        {t('ui6OrgsCanvas.delete_section')}
        {others.length > 0 && section.roster.length > 0 && (
          <select aria-label={t('ui6OrgsCanvas.move_roles_label')} value={moveTo} className="filter-select" style={selectStyle} onChange={e => setMoveTo(e.target.value)}>
            {others.map(s => <option key={s.name} value={s.name}>{t('ui6OrgsCanvas.move_roles_to', { name: s.name })}</option>)}
          </select>
        )}
        <button data-testid="section-delete" style={{ ...btn, color: 'var(--red)', borderColor: 'var(--red)' }} onClick={() => onDelete(moveTo)}>{t('ui6OrgsCanvas.delete_named', { name: section.name })}</button>
      </div>
    </div>
  )
}

export function RoleSectionControls({ node, sections, onAssign, onMakeLead }) {
  const { t } = useTranslation()
  if (!node) return null
  const current = sectionOfRole(sections, node.id)
  const isEndpoint = node.rest?.kind === 'endpoint'
  const isRoot = node.parentId == null
  const lead = sections.find(s => s.name === current)?.leadId === node.id
  return (
    <div data-testid="role-section-controls" style={{ padding: '10px 12px', borderBottom: '1px solid var(--border)', display: 'flex', flexDirection: 'column', gap: 6 }}>
      {isRoot ? (
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>{t('ui6OrgsCanvas.root_note')}</span>
      ) : isEndpoint ? null : (
        <>
          <label style={field}>
            {t('ui6OrgsCanvas.section')}
            <select data-testid="role-section" value={current} className="filter-select" style={selectStyle} onChange={e => e.target.value && onAssign(e.target.value)}>
              {!current && <option value="">{t('ui6OrgsCanvas.no_section_option')}</option>}
              {sections.map(s => <option key={s.name} value={s.name}>{s.name}</option>)}
            </select>
          </label>
          {current && (lead
            ? <span data-testid="role-is-lead" style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--yellow, #eab308)' }}>{t('ui6OrgsCanvas.lead_of', { name: current })}</span>
            : <button data-testid="make-lead" style={btn} onClick={() => onMakeLead(current)}>{t('ui6OrgsCanvas.make_lead', { name: current })}</button>)}
        </>
      )}
    </div>
  )
}
