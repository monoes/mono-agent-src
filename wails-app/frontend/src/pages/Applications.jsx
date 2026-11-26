import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { Plus, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { statusWord } from '../components/applications/constants.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { confirm } from '../components/ConfirmDialog.jsx'
import { notify } from '../services/api.js'
import ApplicationsProcessPendingFlow from './ApplicationsProcessPendingFlow.jsx'
import ApplicationDetailModal from '../components/applications/ApplicationDetailModal.jsx'
import ApplyQueueButton from '../components/applications/ApplyQueueButton.jsx'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'
import { useDialog } from './connections/ui.jsx'
import t from '../lib/i18nOr.js'

const STATUS_TABS = ['pending', 'awaiting_submit', 'needs_you', 'applied', 'rejected', 'cancelled', 'all']

const STATUS_KEYS = {
  all: 'ui6PagesApps.status_all',
  pending: 'ui6PagesApps.status_pending',
  applied: 'ui6PagesApps.status_applied',
  rejected: 'ui6PagesApps.status_rejected',
  cancelled: 'ui6PagesApps.status_cancelled',
  awaiting_submit: 'ui6PagesApps.status_awaiting_submit',
  needs_you: 'ui6PagesApps.status_needs_you',
}
const KIND_KEYS = { job: 'ui6PagesApps.kind_job', tender: 'ui6PagesApps.kind_tender' }

const STATUS_COLORS = {
  pending: { bg: 'rgba(245,158,11,0.12)', color: '#f59e0b' },
  applied: { bg: 'rgba(16,185,129,0.12)', color: '#10b981' },
  rejected: { bg: 'rgba(239,68,68,0.12)', color: '#ef4444' },
  cancelled: { bg: 'rgba(100,116,139,0.12)', color: '#64748b' },
  awaiting_submit: { bg: 'rgba(0,180,216,0.15)', color: '#00b4d8' },
  needs_you: { bg: 'rgba(244,63,94,0.15)', color: '#f43f5e' },
}

// Pure so it's directly unit-testable (see Applications.test.js) without
// rendering the component -- mirrors Vault.jsx's filterAndSortEntries
// convention.
export function filterApplications(applications, { statusTab = 'all', search = '' } = {}) {
  const q = search.trim().toLowerCase()
  return applications.filter(a => {
    if (statusTab !== 'all' && a.status !== statusTab) return false
    if (!q) return true
    return (
      a.title?.toLowerCase().includes(q) ||
      a.company?.toLowerCase().includes(q) ||
      a.tags?.some(t => t.toLowerCase().includes(q))
    )
  })
}

const inputStyle = {
  background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}
const headerBtnStyle = {
  background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
  borderRadius: 6, padding: '6px 12px', color: '#00b4d8',
  fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
  display: 'flex', alignItems: 'center', gap: 5,
}

function StatusBadge({ status }) {
  useTranslation()
  const s = STATUS_COLORS[status] || { bg: '#1a2332', color: '#64748b' }
  return (
    <span style={{ background: s.bg, borderRadius: 3, padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color }}>
      {statusWord(STATUS_KEYS, status)}
    </span>
  )
}

function emptyAddForm() {
  return { kind: 'job', title: '', company: '', url: '', issuingOrg: '', submissionDeadline: '' }
}

// Add form overlay: a real dialog (role, Escape, focus in/out, labelled inputs).
function AddApplicationDialog({ addForm, setAddForm, onSubmit, onClose }) {
  useTranslation()
  const dialog = useDialog(onClose)
  const set = (k) => (e) => setAddForm(f => ({ ...f, [k]: e.target.value }))
  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && onClose()} style={{ position: 'fixed', inset: 0, zIndex: 9000, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
      <form {...dialog} role="dialog" aria-modal="true" aria-labelledby="add-application-title" onSubmit={onSubmit} style={{ background: 'var(--surface, #0d1520)', border: '1px solid rgba(255,255,255,0.1)', borderRadius: 10, padding: 20, width: 400, fontFamily: 'var(--font-mono)', display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h3 id="add-application-title" style={{ margin: 0, fontSize: 14 }}>{t('ui6PagesApps.add_title')}</h3>
        <select aria-label={t('ui6PagesApps.kind')} className="select-compact" style={{ fontSize: 11 }} value={addForm.kind} onChange={set('kind')}>
          <option value="job">{t('ui6PagesApps.job')}</option>
          <option value="tender">{t('ui6PagesApps.tender')}</option>
        </select>
        <input aria-label={t('ui6PagesApps.field_title')} style={inputStyle} placeholder={t('ui6PagesApps.field_title')} required value={addForm.title} onChange={set('title')} />
        <input aria-label={t('ui6PagesApps.field_url')} style={inputStyle} placeholder={t('ui6PagesApps.field_url')} required value={addForm.url} onChange={set('url')} />
        {addForm.kind === 'job' ? (
          <input aria-label={t('ui6PagesApps.field_company')} style={inputStyle} placeholder={t('ui6PagesApps.field_company')} required value={addForm.company} onChange={set('company')} />
        ) : (
          <>
            <input aria-label={t('ui6PagesApps.field_org')} style={inputStyle} placeholder={t('ui6PagesApps.field_org')} required value={addForm.issuingOrg} onChange={set('issuingOrg')} />
            <input aria-label={t('ui6PagesApps.field_deadline')} style={inputStyle} placeholder={t('ui6PagesApps.field_deadline')} required value={addForm.submissionDeadline} onChange={set('submissionDeadline')} />
          </>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 8 }}>
          <button type="button" style={headerBtnStyle} onClick={onClose}>{t('ui6PagesApps.cancel')}</button>
          <button type="submit" style={{ ...headerBtnStyle, background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }}>{t('ui6PagesApps.add')}</button>
        </div>
      </form>
    </div>
  )
}

export default function Applications({ isActive = true, navData = null, onNavigate, onOpenCoderChat }) {
  useTranslation()
  const [applications, setApplications] = useState([])
  const [statusTab, setStatusTab] = useState('pending')
  const [search, setSearch] = useState('')
  const [error, setError] = useState(null)
  const [selectedId, setSelectedId] = useState(null)
  const [detail, setDetail] = useState(null)
  const [showAdd, setShowAdd] = useState(false)
  const [addForm, setAddForm] = useState(emptyAddForm())
  const [showProcessPending, setShowProcessPending] = useState(false)
  // Applications ticked for "Apply selected with coder".
  const [picked, setPicked] = useState(() => new Set())

  const load = useCallback(async () => {
    try {
      const list = await WailsApp.GetApplications('', '', '')
      setApplications(list || [])
    } catch (e) {
      setError(t('ui6PagesApps.load_failed', { error: String(e) }))
    }
  }, [])

  useEffect(() => { load() }, [load])
  useReloadOnActivate(isActive, load)

  // A link from another page (the History drawer) names one application.
  useEffect(() => {
    const id = navData && navData.application
    if (isActive && id) openDetail(String(id))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isActive, navData])

  const visible = useMemo(
    () => filterApplications(applications, { statusTab, search }),
    [applications, statusTab, search]
  )

  // The queue takes applications still being worked on; only those get a tick box.
  const queueable = (a) => a.status === 'pending' || a.status === 'needs_you'
  const pickedIds = visible.filter(a => picked.has(a.id) && queueable(a)).map(a => a.id)
  const togglePicked = (id) => setPicked(cur => {
    const next = new Set(cur)
    if (next.has(id)) next.delete(id); else next.add(id)
    return next
  })

  // Only the latest click may write the detail: a slow answer for an
  // earlier row must not replace it.
  const detailSeq = useRef(0)
  const openDetail = async (id) => {
    const seq = ++detailSeq.current
    setSelectedId(id)
    setDetail(null)
    try {
      const d = await WailsApp.GetApplication(id)
      if (seq === detailSeq.current) setDetail(d)
    } catch (e) {
      if (seq === detailSeq.current) setError(t('ui6PagesApps.detail_failed', { error: String(e) }))
    }
  }

  const closeDetail = () => { setSelectedId(null); setDetail(null) }

  const handleAdd = async (e) => {
    e.preventDefault()
    setError(null)
    try {
      await WailsApp.AddApplication(
        addForm.kind, addForm.title, addForm.company, addForm.url,
        addForm.issuingOrg, addForm.submissionDeadline,
      )
      setAddForm(emptyAddForm())
      setShowAdd(false)
      load()
    } catch (e) {
      setError(t('ui6PagesApps.add_failed', { error: String(e) }))
    }
  }

  const handleSetStatus = async (id, status) => {
    if (status === 'cancelled' && !(await confirm(t('ui6PagesApps.cancel_question'), { title: t('ui6PagesApps.cancel_application'), confirmLabel: t('ui6PagesApps.cancel_application') }))) return
    try {
      await WailsApp.SetApplicationStatus(id, status, '')
      await load()
      if (selectedId === id) openDetail(id)
    } catch (e) {
      notify(t('ui6PagesApps.op_status'), e?.message || String(e))
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', padding: 16, gap: 12, overflow: 'hidden' }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
        <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text-primary)', whiteSpace: 'nowrap' }}>{t('ui6PagesApps.applications')}</h2>
        <div style={{ flex: 1 }} />
        <button style={headerBtnStyle} onClick={() => setShowAdd(true)}><Plus size={13} /> {t('ui6PagesApps.add')}</button>
        {visible.some(queueable) && (
          <ApplyQueueButton ids={pickedIds} style={headerBtnStyle} onStarted={() => { setPicked(new Set()); load() }} />
        )}
        <button style={{ ...headerBtnStyle, background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }} onClick={() => setShowProcessPending(true)}>
          {t('ui6PagesApps.process_pending')}
        </button>
      </div>

      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
        {STATUS_TABS.map(tab => (
          <button
            key={tab}
            onClick={() => setStatusTab(tab)}
            style={{
              padding: '5px 12px', borderRadius: 5, cursor: 'pointer', fontSize: 11, fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap',
              background: statusTab === tab ? 'rgba(0,180,216,0.15)' : 'transparent',
              border: `1px solid ${statusTab === tab ? 'rgba(0,180,216,0.4)' : 'rgba(255,255,255,0.1)'}`,
              color: statusTab === tab ? '#00b4d8' : 'var(--text-secondary)',
            }}
          >{statusWord(STATUS_KEYS, tab)}</button>
        ))}
        <input style={{ ...inputStyle, flex: '1 1 160px', minWidth: 0, marginLeft: 8 }} placeholder={t('ui6PagesApps.search')} value={search} onChange={e => setSearch(e.target.value)} />
      </div>

      {error && <div style={{ color: '#ff6b6b', fontSize: 12 }}>{error}</div>}

      <div style={{ flex: 1, overflow: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead>
            <tr style={{ textAlign: 'left', color: 'var(--text-muted)', fontSize: 10, textTransform: 'uppercase' }}>
              <th style={{ padding: '6px 8px', width: 24 }}><span className="sr-only" style={{ position: 'absolute', left: -9999 }}>{t('ui6PagesApps.select')}</span></th>
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesApps.field_title')}</th>
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesApps.col_company')}</th>
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesApps.kind')}</th>
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesApps.col_status')}</th>
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesApps.col_tags')}</th>
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesApps.col_updated')}</th>
            </tr>
          </thead>
          <tbody>
            {visible.map(a => (
              <tr key={a.id} onClick={() => openDetail(a.id)} style={{ cursor: 'pointer', borderTop: '1px solid rgba(255,255,255,0.05)' }}>
                <td style={{ padding: '8px' }} onClick={e => e.stopPropagation()}>
                  {queueable(a) && (
                    <input type="checkbox" aria-label={t('ui6PagesApps.select_apply', { title: a.title })} checked={picked.has(a.id)} onChange={() => togglePicked(a.id)} />
                  )}
                </td>
                <td style={{ padding: '8px' }}>{a.title}</td>
                <td style={{ padding: '8px' }}>{a.company}</td>
                <td style={{ padding: '8px' }}>{statusWord(KIND_KEYS, a.kind || '')}</td>
                <td style={{ padding: '8px' }}>
                  <StatusBadge status={a.status} />
                  {a.status === 'awaiting_submit' && <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 2 }}>{t('ui6PagesApps.waiting_submit')}</div>}
                  {a.status === 'needs_you' && <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 2 }}>{t('ui6PagesApps.reopen_refill')}</div>}
                </td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }}>{(a.tags || []).join(', ')}</td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }}>{a.updated_at}</td>
              </tr>
            ))}
            {visible.length === 0 && (
              <tr><td colSpan={7} style={{ padding: 16, textAlign: 'center', color: 'var(--text-muted)' }}>{t('ui6PagesApps.none')}</td></tr>
            )}
          </tbody>
        </table>
      </div>

      {selectedId && detail && (
        <ApplicationDetailModal
          applicationId={selectedId}
          detail={detail}
          onClose={closeDetail}
          onSetStatus={handleSetStatus}
          onNavigate={onNavigate}
          onOpenCoderChat={onOpenCoderChat}
          onChanged={async () => { await load(); openDetail(selectedId) }}
        />
      )}

      {showAdd && (
        <AddApplicationDialog
          addForm={addForm}
          setAddForm={setAddForm}
          onSubmit={handleAdd}
          onClose={() => setShowAdd(false)}
        />
      )}

      {showProcessPending && (
        <ApplicationsProcessPendingFlow
          pendingApplications={applications.filter(a => a.status === 'pending')}
          onClose={() => { setShowProcessPending(false); load() }}
        />
      )}
    </div>
  )
}
