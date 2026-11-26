// Read-only Documents panel for one org run (#339): the document channel of a
// sections org. Documents by producing section and type, status, producer ->
// consumer, round N of the rework cap, the lineage of each revise cycle,
// deliverables, and a badge when a cap is hit. No messaging: documents are the
// only channel between sections, and this panel only shows them.
import { AlertTriangle, ArrowRight, FileText } from 'lucide-react'
import { Chip, mono, mutedText, sectionLabel } from '../orgs/ui.jsx'
import useOrgDocuments from './useOrgDocuments.js'
import { statusMeta, versionLabel } from './orgDocuments.js'
import { useTranslation } from 'react-i18next'

const box = { background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 12px' }

export function CapBadge({ doc }) {
  const { t: tr } = useTranslation()
  const t = (doc.threads || []).find(x => x.exhausted)
  if (!t) return null
  return (
    <span
      role="status"
      title={tr(t.frozen ? 'ui6OrgsCanvas.cap_title_frozen' : 'ui6OrgsCanvas.cap_title_decided', { consumer: t.consumer, rounds: t.rounds, cap: t.cap })}
      style={{ ...mono, display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10, fontWeight: 700, color: '#fff', background: 'var(--red, #ef4444)', borderRadius: 8, padding: '0 8px', lineHeight: '16px' }}
    >
      <AlertTriangle size={10} aria-hidden="true" /> {tr(t.frozen ? 'ui6OrgsCanvas.cap_hit_frozen' : 'ui6OrgsCanvas.cap_hit_badge')}
    </span>
  )
}

const decisionKey = (d) => {
  if (d.decision === 'accept') return d.override ? 'ui6OrgsCanvas.decision_accepted_override' : 'ui6OrgsCanvas.decision_accepted'
  return d.override ? 'ui6OrgsCanvas.decision_rejected_override' : 'ui6OrgsCanvas.decision_rejected'
}

function Lineage({ doc }) {
  const { t } = useTranslation()
  const vs = doc.versions || []
  return (
    <ol aria-label={t('ui6OrgsCanvas.versions_of', { id: doc.id })} style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 3 }}>
      {vs.map(v => {
        const m = statusMeta(v.status)
        const decisions = Object.entries(v.decisions || {})
        return (
          <li key={v.version} style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)', display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'baseline' }}>
            <b style={{ color: 'var(--text)' }}>v{v.version}</b>
            <span style={{ color: m.color }}>{versionLabel(v)}</span>
            {v.supersedes ? <span style={mutedText}>{t('ui6OrgsCanvas.replaces_version', { version: v.supersedes })}</span> : null}
            {(v.waiting_on || []).length > 0 && <span style={mutedText}>{t('ui6OrgsCanvas.waiting_on', { names: v.waiting_on.join(', ') })}</span>}
            {decisions.map(([consumer, d]) => (
              <span key={consumer} style={mutedText}>
                {t(decisionKey(d), { consumer, by: d.by })}{d.reason ? ` — ${d.reason}` : ''}{d.relayed_at ? t('ui6OrgsCanvas.relayed') : ''}
              </span>
            ))}
            {v.note && <span style={mutedText}>{t('ui6OrgsCanvas.note', { note: v.note })}</span>}
          </li>
        )
      })}
    </ol>
  )
}

function DocCard({ doc }) {
  const { t } = useTranslation()
  const m = statusMeta(doc.status)
  const hasRounds = (doc.threads || []).length > 0
  return (
    <li style={{ ...box, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }} data-doc={doc.id}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 }}>
        <FileText size={12} aria-hidden="true" style={{ color: 'var(--text-muted)' }} />
        <span style={{ ...mono, fontSize: 12, color: 'var(--text)', fontWeight: 600 }}>{doc.id}</span>
        <Chip>{doc.type}</Chip>
        <Chip color={m.color}>{m.label}</Chip>
        {hasRounds && <Chip color={doc.cap_hit ? 'var(--red, #ef4444)' : '#eab308'} title={t('ui6OrgsCanvas.rejections_title')}>{t('ui6OrgsCanvas.round_of', { round: doc.round, cap: doc.cap })}</Chip>}
        <CapBadge doc={doc} />
      </div>
      <div style={{ ...mutedText, display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
        <span>{doc.producer}</span><ArrowRight size={10} aria-label={t('ui6OrgsCanvas.to')} />
        <span>{(doc.consumers || []).join(', ') || t('ui6OrgsCanvas.no_consumer')}</span>
        <span>· v{doc.head}</span>
      </div>
      <Lineage doc={doc} />
      {(doc.deliverables || []).length > 0 && (
        <div style={{ ...mutedText }}>{t('ui6OrgsCanvas.deliverables')} {doc.deliverables.map(f => <code key={f} style={{ marginRight: 6 }}>{f}</code>)}</div>
      )}
    </li>
  )
}

function SummaryBar({ s, view }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
      <span style={sectionLabel}>{t('ui6OrgsCanvas.documents')}</span>
      <Chip>{t('ui6OrgsCanvas.docs_total', { total: s.documents || 0 })}</Chip>
      {s.pending > 0 && <Chip color={statusMeta('pending').color}>{t('ui6OrgsCanvas.docs_published', { total: s.pending })}</Chip>}
      {s.accepted > 0 && <Chip color={statusMeta('accepted').color}>{t('ui6OrgsCanvas.docs_accepted', { total: s.accepted })}</Chip>}
      {s.rejected > 0 && <Chip color={statusMeta('rejected').color}>{t('ui6OrgsCanvas.docs_rejected', { total: s.rejected })}</Chip>}
      {s.reworking > 0 && <Chip color="#eab308">{t('ui6OrgsCanvas.docs_rework', { total: s.reworking })}</Chip>}
      {s.cap_hit > 0 && <Chip color="var(--red, #ef4444)">{t('ui6OrgsCanvas.docs_cap_hit', { total: s.cap_hit })}</Chip>}
      {view.run && <span style={{ ...mutedText, marginLeft: 'auto' }}>{view.run}</span>}
    </div>
  )
}

export default function DocumentsPanel({ orgName, run = '', live = false }) {
  const { t } = useTranslation()
  const { view, loading, error } = useOrgDocuments({ orgName, run, live })
  if (!view) {
    if (error) return <div role="alert" style={{ ...mutedText, color: 'var(--red, #ef4444)' }}>{error}</div>
    return <div style={mutedText}>{loading ? t('ui6OrgsCanvas.loading_documents') : t('ui6OrgsCanvas.no_documents')}</div>
  }
  const byId = Object.fromEntries(view.docs.map(d => [d.id, d]))
  const groups = view.sections.filter(g => (g.types || []).length > 0)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <SummaryBar s={view.summary} view={view} />
      {view.integrity && (
        <div role="alert" style={{ ...mono, fontSize: 10.5, color: '#eab308' }}>
          {t('ui6OrgsCanvas.integrity_failed', { reason: view.integrity })}
        </div>
      )}
      {view.docs.length === 0 && (
        <div style={mutedText}>{t('ui6OrgsCanvas.run_no_documents')}</div>
      )}
      {groups.map(g => (
        <section key={g.section} aria-label={t('ui6OrgsCanvas.section_named', { name: g.section })} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
            <span style={sectionLabel}>{g.section}</span>
            <span style={mutedText}>{t('ui6OrgsCanvas.produces')}</span>
          </div>
          {g.types.map(tg => (
            <div key={tg.type} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={{ ...mutedText, color: 'var(--text-secondary)' }}>{tg.type}</span>
              <ul style={{ margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
                {tg.docs.map(id => byId[id] && <DocCard key={id} doc={byId[id]} />)}
              </ul>
            </div>
          ))}
        </section>
      ))}
    </div>
  )
}
