// Recordings tab: recordings linked to this automation (provenance) and new
// recordings not saved anywhere yet. Analyze opens the review flow.
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import '../../i18n.js'
import { RefreshCw, Sparkles, Trash2 } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { confirm } from '../../components/ConfirmDialog.jsx'
import { Chip, ErrorBox, Busy, body, label, mono, muted, panel, fmtDate } from './ui.jsx'
import RecordingReview from './RecordingReview.jsx'

function Row({ r, onAnalyze, onDelete }) {
  const { t } = useTranslation()
  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <span title={r.title || r.url || r.id} style={{ ...mono, fontSize: 11.5, fontWeight: 700, color: 'var(--text)', flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.title || r.url || r.id}</span>
        {!r.complete && <Chip color="var(--yellow)" title={r.stopReason || ''}>{t('ui6ConnAuto.incomplete')}</Chip>}
        {r.automation && <Chip color="var(--teal)">{t('ui6ConnAuto.saved')}</Chip>}
      </div>
      {r.goal && <div style={{ ...body, fontSize: 11 }}>“{r.goal}”</div>}
      <div title={r.url} style={{ ...muted, fontSize: 10, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {fmtDate(r.startedAt)} · {t('ui6ConnAuto.events', { count: r.events || 0 })} · {r.url}
      </div>
      <div style={{ display: 'flex', gap: 6 }}>
        <button className="btn btn-primary btn-sm" onClick={onAnalyze} style={{ gap: 5 }}><Sparkles size={11} /> {r.automation ? t('ui6ConnAuto.analyze_again') : t('ui6ConnAuto.analyze')}</button>
        <button className="btn btn-ghost btn-sm" onClick={onDelete} style={{ gap: 5 }}><Trash2 size={11} /> {t('ui6ConnAuto.delete')}</button>
      </div>
    </div>
  )
}

export default function RecordingsTab({ automationId, onSaved }) {
  const { t } = useTranslation()
  const [res, setRes] = useState(null)
  const [loading, setLoading] = useState(false)
  const [reviewing, setReviewing] = useState(null)

  const load = useCallback(async () => {
    setLoading(true)
    try { setRes(await api.listRecordings()) } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  const del = async (r) => {
    if (!(await confirm(t('ui6ConnAuto.delete_confirm', { name: r.title || r.id })))) return
    const out = await api.deleteRecording(r.id)
    if (out?.error) notify(t('ui6ConnAuto.op_delete_recording'), out.error)
    await load()
  }

  if (reviewing) {
    return (
      <RecordingReview
        recording={reviewing}
        automationId={automationId}
        onBack={() => { setReviewing(null); load() }}
        onSaved={onSaved}
      />
    )
  }

  const all = res?.recordings || []
  const linked = all.filter(r => r.automation === automationId)
  const fresh = all.filter(r => !r.automation)
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ ...body, flex: 1, fontSize: 11 }}>{t('ui6ConnAuto.recordings_intro')}</span>
        <button className="btn btn-ghost btn-sm" onClick={load} disabled={loading} style={{ gap: 5 }}><RefreshCw size={11} /> {t('ui6Conn.refresh')}</button>
      </div>
      {loading && !res && <Busy text={t('ui6ConnAuto.loading_recordings')} />}
      {res?.error && <ErrorBox>{res.error}</ErrorBox>}
      <span style={label}>{t('ui6ConnAuto.saved_into', { total: linked.length })}</span>
      {linked.length ? linked.map(r => <Row key={r.id} r={r} onAnalyze={() => setReviewing(r)} onDelete={() => del(r)} />) : <span style={muted}>{t('ui6ConnAuto.none_yet')}</span>}
      <span style={label}>{t('ui6ConnAuto.new_recordings', { total: fresh.length })}</span>
      {fresh.length ? fresh.map(r => <Row key={r.id} r={r} onAnalyze={() => setReviewing(r)} onDelete={() => del(r)} />) : <span style={muted}>{t('ui6ConnAuto.none')}</span>}
    </>
  )
}
