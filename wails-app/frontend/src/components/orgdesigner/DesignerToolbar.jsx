// Org Designer toolbar: org name and validation, Design / Live / Grants view
// switch, the run picker while Live, and the design-only actions.
import { useState } from 'react'
import { Maximize2, Minimize2, Milestone, RefreshCw, PencilRuler, Activity, Grid3x3, Workflow } from 'lucide-react'
import { pendingGates, pendingCounts } from './orgActivity.js'
import ScheduleControl from './ScheduleControl.jsx'
import { useTranslation } from 'react-i18next'

const VIEWS = [
  { id: 'design', labelKey: 'ui6OrgsDesigner.view_design', icon: PencilRuler, hintKey: 'ui6OrgsDesigner.view_design_hint' },
  { id: 'live', labelKey: 'ui6OrgsDesigner.view_live', icon: Activity, hintKey: 'ui6OrgsDesigner.view_live_hint' },
  { id: 'matrix', labelKey: 'ui6OrgsDesigner.view_matrix', icon: Grid3x3, hintKey: 'ui6OrgsDesigner.view_matrix_hint' },
]

export const toolbarBtnStyle = {
  display: 'flex', alignItems: 'center', gap: 4,
  fontFamily: 'var(--font-mono)', fontSize: 10, padding: '4px 8px', borderRadius: 'var(--radius)',
  background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'pointer',
}

function LiveSummary({ state }) {
  const { t } = useTranslation()
  if (!state) return null
  const counts = pendingCounts(state)
  const gates = pendingGates(state)
  const working = Object.values(state.roles).filter(r => r.status === 'working').length
  const parts = [t('ui6OrgsDesigner.working_count', { total: working })]
  if (gates.length) parts.push(t('ui6OrgsDesigner.gates', { count: gates.length }))
  if (counts.approvals + counts.questions) parts.push(t('ui6OrgsDesigner.waiting_count', { total: counts.approvals + counts.questions }))
  if (state.usage.costUsd) parts.push(`$${state.usage.costUsd.toFixed(2)}`)
  return <span data-testid="live-summary" style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)' }}>{parts.join(' · ')}</span>
}

// ValidationIssues is the toolbar's "N issues" count; clicking it lists
// them: the structural checks plus `org validate`'s own, which include
// full-access taint problems (#205).
export function ValidationIssues({ errors }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <span style={{ position: 'relative' }}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(o => !o)}
        style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: '#f87171', background: 'transparent', border: 'none', padding: 0, cursor: 'pointer', textDecoration: 'underline dotted' }}
      >
        {t('ui6OrgsDesigner.issues', { count: errors.length })}
      </button>
      {open && (
        <ul data-testid="validation-issues" style={{
          position: 'absolute', top: '100%', left: 0, marginTop: 4, zIndex: 30, width: 420, maxWidth: '70vw',
          listStyle: 'none', padding: 8, margin: 0, display: 'flex', flexDirection: 'column', gap: 5,
          background: 'var(--surface, #0d1520)', border: '1px solid rgba(248,113,113,0.35)', borderRadius: 8,
          boxShadow: '0 8px 24px rgba(0,0,0,0.45)',
        }}>
          {errors.map((e, i) => (
            <li key={i} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#fca5a5', lineHeight: 1.5, wordBreak: 'break-word' }}>{e}</li>
          ))}
        </ul>
      )}
    </span>
  )
}

export default function DesignerToolbar({
  orgName, validation, pendingUpdateCount, onApplyPending,
  viewMode, onViewMode, liveSource, runs = [], onLiveSource, live,
  onAddRole, onAddSection, onTidy, edgeStyle, onToggleEdgeStyle, onReload, fullscreen, onToggleFullscreen, onOpenAutomations,
  schedule, sectionsOrg = false, onSetSchedule,
}) {
  const { t } = useTranslation()
  const design = viewMode === 'design'
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', borderBottom: '1px solid var(--border)', flexShrink: 0, flexWrap: 'wrap' }}>
      <Milestone size={12} style={{ color: 'var(--text-muted)' }} />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-secondary)' }}>{orgName}</span>
      {!validation.valid && <ValidationIssues errors={validation.errors} />}
      {pendingUpdateCount > 0 && (
        <button
          onClick={onApplyPending}
          style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--teal, #00f5d4)', background: 'transparent', border: '1px solid currentColor', borderRadius: 4, padding: '2px 6px', cursor: 'pointer' }}
        >
          {t('ui6OrgsDesigner.updates_pending', { count: pendingUpdateCount })}
        </button>
      )}

      <div role="tablist" aria-label={t('ui6OrgsDesigner.canvas_view')} style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 'var(--radius)', overflow: 'hidden', marginLeft: 6 }}>
        {VIEWS.map(v => {
          const Icon = v.icon
          const active = viewMode === v.id
          return (
            <button
              key={v.id}
              role="tab"
              aria-selected={active}
              title={t(v.hintKey)}
              onClick={() => onViewMode(v.id)}
              style={{
                display: 'flex', alignItems: 'center', gap: 4, fontFamily: 'var(--font-mono)', fontSize: 10, padding: '4px 8px',
                border: 'none', cursor: 'pointer',
                background: active ? 'var(--elevated)' : 'transparent', color: active ? 'var(--text)' : 'var(--text-muted)',
              }}
            >
              <Icon size={10} /> {t(v.labelKey)}
            </button>
          )
        })}
      </div>

      {viewMode === 'live' && (
        <>
          <select
            aria-label={t('ui6OrgsDesigner.run_to_show')}
            className="filter-select"
            value={liveSource}
            onChange={e => onLiveSource(e.target.value)}
            style={{ fontSize: 10, padding: '2px 22px 2px 6px', maxWidth: 240 }}
          >
            <option value="live">{t('ui6OrgsDesigner.live_current')}</option>
            {runs.map(r => <option key={r} value={r}>{t('ui6OrgsDesigner.replay_run', { run: r })}</option>)}
          </select>
          {live?.loading && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)' }}>{t('ui6OrgsDesigner.loading')}</span>}
          {live?.error && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: '#f87171' }}>{live.error}</span>}
          {live?.noRun && <span data-testid="live-no-run" style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)' }}>{t('ui6OrgsDesigner.no_run_yet')}</span>}
          {!live?.loading && !live?.noRun && <LiveSummary state={live?.state} />}
        </>
      )}

      <div style={{ flex: 1 }} />
      <button onClick={onOpenAutomations} title={t('ui6OrgsDesigner.automations_title')} style={toolbarBtnStyle}><Workflow size={11} /> {t('ui6OrgsDesigner.automations')}</button>
      {onSetSchedule && <ScheduleControl schedule={schedule} sections={sectionsOrg} onSave={onSetSchedule} />}
      {design && <button onClick={onAddRole} title={t('ui6OrgsDesigner.add_role_title')} style={toolbarBtnStyle}>{t('ui6OrgsDesigner.add_role')}</button>}
      {design && onAddSection && <button onClick={onAddSection} title={t('ui6OrgsDesigner.add_section_title')} style={toolbarBtnStyle}>{t('ui6OrgsDesigner.add_section')}</button>}
      {design && <button onClick={onTidy} title={t('ui6OrgsDesigner.tidy_title')} style={toolbarBtnStyle}>{t('ui6OrgsDesigner.tidy')}</button>}
      {viewMode !== 'matrix' && (
        <button onClick={onToggleEdgeStyle} title={t('ui6OrgsDesigner.connector_style')} style={toolbarBtnStyle}>
          {edgeStyle === 'bezier' ? t('ui6OrgsDesigner.curved') : t('ui6OrgsDesigner.elbow')}
        </button>
      )}
      <button onClick={onReload} title={t('ui6OrgsDesigner.reload')} aria-label={t('ui6OrgsDesigner.reload')} style={toolbarBtnStyle}><RefreshCw size={11} /></button>
      {onToggleFullscreen && (
        <button onClick={onToggleFullscreen} title={fullscreen ? t('ui6OrgsDesigner.exit_fullscreen') : t('ui6OrgsDesigner.fullscreen')} style={toolbarBtnStyle}>
          {fullscreen ? <Minimize2 size={11} /> : <Maximize2 size={11} />}
        </button>
      )}
    </div>
  )
}
