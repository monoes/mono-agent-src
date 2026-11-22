import { useState, useEffect, useCallback, useMemo } from 'react'
import { Plus, ExternalLink } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { confirm } from '../components/ConfirmDialog.jsx'
import { notify } from '../services/api.js'
import ApplicationsProcessPendingFlow from './ApplicationsProcessPendingFlow.jsx'
import ApplicationDetailModal from '../components/applications/ApplicationDetailModal.jsx'
import ApplyQueueButton from '../components/applications/ApplyQueueButton.jsx'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'
import { useDialog } from './connections/ui.jsx'

const STATUS_TABS = ['pending', 'awaiting_submit', 'needs_you', 'applied', 'rejected', 'cancelled', 'all']

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
  const s = STATUS_COLORS[status] || { bg: '#1a2332', color: '#64748b' }
  return (
    <span style={{ background: s.bg, borderRadius: 3, padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color }}>
      {status.replace(/_/g, ' ')}
    </span>
  )
}

function emptyAddForm() {
  return { kind: 'job', title: '', company: '', url: '', issuingOrg: '', submissionDeadline: '' }
}

// Add form overlay: a real dialog (role, Escape, focus in/out, labelled inputs).
function AddApplicationDialog({ addForm, setAddForm, onSubmit, onClose }) {
  const dialog = useDialog(onClose)
  const set = (k) => (e) => setAddForm(f => ({ ...f, [k]: e.target.value }))
  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && onClose()} style={{ position: 'fixed', inset: 0, zIndex: 9000, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
      <form {...dialog} role="dialog" aria-modal="true" aria-labelledby="add-application-title" onSubmit={onSubmit} style={{ background: 'var(--surface, #0d1520)', border: '1px solid rgba(255,255,255,0.1)', borderRadius: 10, padding: 20, width: 400, fontFamily: 'var(--font-mono)', display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h3 id="add-application-title" style={{ margin: 0, fontSize: 14 }}>Add Application</h3>
        <select aria-label="Kind" className="select-compact" style={{ fontSize: 11 }} value={addForm.kind} onChange={set('kind')}>
          <option value="job">Job</option>
          <option value="tender">Tender</option>
        </select>
        <input aria-label="Title" style={inputStyle} placeholder="Title" required value={addForm.title} onChange={set('title')} />
        <input aria-label="URL" style={inputStyle} placeholder="URL" required value={addForm.url} onChange={set('url')} />
        {addForm.kind === 'job' ? (
          <input aria-label="Company" style={inputStyle} placeholder="Company" required value={addForm.company} onChange={set('company')} />
        ) : (
          <>
            <input aria-label="Issuing Organization" style={inputStyle} placeholder="Issuing Organization" required value={addForm.issuingOrg} onChange={set('issuingOrg')} />
            <input aria-label="Submission Deadline" style={inputStyle} placeholder="Submission Deadline" required value={addForm.submissionDeadline} onChange={set('submissionDeadline')} />
          </>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 8 }}>
          <button type="button" style={headerBtnStyle} onClick={onClose}>Cancel</button>
          <button type="submit" style={{ ...headerBtnStyle, background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }}>Add</button>
        </div>
      </form>
    </div>
  )
}

export default function Applications({ isActive = true, navData = null, onNavigate, onOpenCoderChat }) {
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
      setError('Failed to load applications: ' + e)
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

  const openDetail = async (id) => {
    setSelectedId(id)
    try {
      const d = await WailsApp.GetApplication(id)
      setDetail(d)
    } catch (e) {
      setError('Failed to load detail: ' + e)
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
      setError('Add failed: ' + e)
    }
  }

  const handleSetStatus = async (id, status) => {
    if (status === 'cancelled' && !(await confirm('Mark this application as cancelled?', { title: 'Cancel Application', confirmLabel: 'Cancel Application' }))) return
    try {
      await WailsApp.SetApplicationStatus(id, status, '')
      await load()
      if (selectedId === id) openDetail(id)
    } catch (e) {
      notify('application status', e?.message || String(e))
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', padding: 16, gap: 12, overflow: 'hidden' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text-primary)' }}>Applications</h2>
        <div style={{ flex: 1 }} />
        <button style={headerBtnStyle} onClick={() => setShowAdd(true)}><Plus size={13} /> Add</button>
        {visible.some(queueable) && (
          <ApplyQueueButton ids={pickedIds} style={headerBtnStyle} onStarted={() => { setPicked(new Set()); load() }} />
        )}
        <button style={{ ...headerBtnStyle, background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }} onClick={() => setShowProcessPending(true)}>
          Process Pending
        </button>
      </div>

      <div style={{ display: 'flex', gap: 8 }}>
        {STATUS_TABS.map(tab => (
          <button
            key={tab}
            onClick={() => setStatusTab(tab)}
            style={{
              padding: '5px 12px', borderRadius: 5, cursor: 'pointer', fontSize: 11, fontFamily: 'var(--font-mono)',
              background: statusTab === tab ? 'rgba(0,180,216,0.15)' : 'transparent',
              border: `1px solid ${statusTab === tab ? 'rgba(0,180,216,0.4)' : 'rgba(255,255,255,0.1)'}`,
              color: statusTab === tab ? '#00b4d8' : 'var(--text-secondary)',
            }}
          >{tab.replace('_', ' ')}</button>
        ))}
        <input style={{ ...inputStyle, flex: 1, marginLeft: 8 }} placeholder="Search title, company, tags..." value={search} onChange={e => setSearch(e.target.value)} />
      </div>

      {error && <div style={{ color: '#ff6b6b', fontSize: 12 }}>{error}</div>}

      <div style={{ flex: 1, overflow: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead>
            <tr style={{ textAlign: 'left', color: 'var(--text-muted)', fontSize: 10, textTransform: 'uppercase' }}>
              <th style={{ padding: '6px 8px', width: 24 }}><span className="sr-only" style={{ position: 'absolute', left: -9999 }}>Select</span></th>
              <th style={{ padding: '6px 8px' }}>Title</th>
              <th style={{ padding: '6px 8px' }}>Company / Org</th>
              <th style={{ padding: '6px 8px' }}>Kind</th>
              <th style={{ padding: '6px 8px' }}>Status</th>
              <th style={{ padding: '6px 8px' }}>Tags</th>
              <th style={{ padding: '6px 8px' }}>Updated</th>
            </tr>
          </thead>
          <tbody>
            {visible.map(a => (
              <tr key={a.id} onClick={() => openDetail(a.id)} style={{ cursor: 'pointer', borderTop: '1px solid rgba(255,255,255,0.05)' }}>
                <td style={{ padding: '8px' }} onClick={e => e.stopPropagation()}>
                  {queueable(a) && (
                    <input type="checkbox" aria-label={`Select ${a.title} to apply with coder`} checked={picked.has(a.id)} onChange={() => togglePicked(a.id)} />
                  )}
                </td>
                <td style={{ padding: '8px' }}>{a.title}</td>
                <td style={{ padding: '8px' }}>{a.company}</td>
                <td style={{ padding: '8px' }}>{a.kind}</td>
                <td style={{ padding: '8px' }}>
                  <StatusBadge status={a.status} />
                  {a.status === 'awaiting_submit' && <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 2 }}>waiting for your submit</div>}
                  {a.status === 'needs_you' && <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 2 }}>reopen and refill</div>}
                </td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }}>{(a.tags || []).join(', ')}</td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }}>{a.updated_at}</td>
              </tr>
            ))}
            {visible.length === 0 && (
              <tr><td colSpan={7} style={{ padding: 16, textAlign: 'center', color: 'var(--text-muted)' }}>No applications.</td></tr>
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
