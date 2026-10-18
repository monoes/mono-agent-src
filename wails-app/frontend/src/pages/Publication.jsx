import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Send, ExternalLink, X } from 'lucide-react'
import { api } from '../services/api.js'

const pageSize = 50
const emptyFilters = { search: '', platform: '', kind: '', workflow: '', agent: '', since: '', until: '' }
const panel = { padding: 16, border: '1px solid var(--border)', borderRadius: 6, background: 'var(--elevated)' }
// Remote content must never supply executable URL schemes to the desktop opener.
const webURL = value => /^https?:\/\//i.test(value || '')
const timeLabel = value => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString() : value

export default function Publication({ isActive = true, profileId, onNavigate }) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(emptyFilters)
  const [filters, setFilters] = useState(emptyFilters)
  const [offset, setOffset] = useState(0)
  const [entries, setEntries] = useState([])
  const [stats, setStats] = useState(null)
  const [hasNext, setHasNext] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [detail, setDetail] = useState(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const dialogRef = useRef(null)
  const request = useRef(0)
  const detailRequest = useRef(0)

  const load = useCallback(async () => {
    const seq = ++request.current
    setLoading(true)
    setError('')
    try {
      const [rows, counts] = await Promise.all([api.listPublications({ ...filters, offset, limit: pageSize + 1 }), api.getPublicationStats()])
      if (seq !== request.current) return
      setEntries((rows || []).slice(0, pageSize))
      setHasNext((rows || []).length > pageSize)
      setStats(counts)
    } catch (e) {
      if (seq === request.current) setError(e?.message || t('publication.loadError'))
    } finally {
      if (seq === request.current) setLoading(false)
    }
  }, [filters, offset, t])

  useEffect(() => {
    if (isActive) load()
    return () => { ++request.current }
  }, [isActive, load, profileId])
  useEffect(() => {
    setDetail(null)
    setDetailError('')
    ++detailRequest.current
    setEntries([])
    setStats(null)
    setOffset(0)
  }, [profileId])
  useEffect(() => () => { ++detailRequest.current }, [])

  useEffect(() => {
    if (!detail) return
    const previousFocus = document.activeElement
    dialogRef.current?.querySelector('button')?.focus()
    return () => previousFocus?.focus?.()
  }, [Boolean(detail)])

  const openDetail = async entry => {
    const seq = ++detailRequest.current
    setDetail(entry)
    setDetailLoading(true)
    setDetailError('')
    try {
      const result = await api.getPublication(entry.id)
      if (seq === detailRequest.current) setDetail(result)
    } catch (e) {
      if (seq === detailRequest.current) setDetailError(e?.message || t('publication.loadError'))
    } finally {
      if (seq === detailRequest.current) setDetailLoading(false)
    }
  }
  const closeDetail = () => { ++detailRequest.current; setDetail(null) }
  const platforms = Object.keys(stats?.by_platform || {}).sort()
  const kinds = Object.keys(stats?.by_kind || {}).sort()

  return <>
    <div className="page-header">
      <div className="page-header-left">
        <div className="page-title">{t('publication.title')}</div>
        <div className="page-subtitle">{t('publication.subtitle', { count: stats?.total || 0 })}</div>
      </div>
      <button className="btn btn-ghost btn-sm" onClick={load} disabled={loading}><RefreshCw size={12} /> {t('publication.refresh')}</button>
    </div>
    <div className="page-body">
      <form onSubmit={e => { e.preventDefault(); setOffset(0); setFilters({ ...draft }) }} style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 16 }}>
        {Object.keys(emptyFilters).map(key => <label key={key} style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11, minWidth: 145, flex: '1 1 145px', maxWidth: 230 }}>
          {t(`publication.filter.${key}`)}
          {(key === 'platform' || key === 'kind') ? <select value={draft[key]} onChange={e => setDraft({ ...draft, [key]: e.target.value })} className="form-select">
            <option value="">{t('publication.all')}</option>
            {(key === 'platform' ? platforms : kinds).map(value => <option key={value}>{value}</option>)}
          </select> : <input className="form-input" style={{ colorScheme: 'dark' }} type={key === 'since' || key === 'until' ? 'date' : 'text'} value={draft[key]} onChange={e => setDraft({ ...draft, [key]: e.target.value })} />}
        </label>)}
        <button className="btn btn-primary btn-sm" type="submit" style={{ alignSelf: 'flex-end' }}>{t('publication.apply')}</button>
      </form>
      {loading ? <div className="empty-state" role="status"><div className="spinner" />{t('publication.loading')}</div>
        : error ? <div role="alert" style={{ ...panel, color: 'var(--red)' }}>{error}</div>
        : entries.length === 0 ? <div className="empty-state"><Send size={28} /><div className="empty-state-title">{t('publication.empty')}</div><p>{t('publication.emptyHint')}</p></div>
        : <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {entries.map(entry => <button key={entry.id} onClick={() => openDetail(entry)} style={{ ...panel, textAlign: 'left', color: 'var(--text)', font: 'inherit', cursor: 'pointer' }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, fontSize: 11, color: 'var(--text-muted)' }}>
              <strong>{entry.platform}</strong><span>{entry.kind}</span><time>{timeLabel(entry.published_at || entry.recorded_at)}</time>{entry.account && <span>{entry.account}</span>}
            </div>
            {entry.title && <div style={{ fontWeight: 600, marginTop: 8 }}>{entry.title}</div>}
            <div style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', marginTop: 6 }}>{(entry.body || entry.url || '').slice(0, 240)}</div>
            <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 8 }}>
              {[entry.agent_id && `${t('publication.agent')}: ${entry.agent_id}`, entry.workflow_id && `${t('publication.workflow')}: ${entry.workflow_id}`].filter(Boolean).join(' · ') || t('publication.external')}
            </div>
          </button>)}
        </div>}
      {!loading && !error && <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginTop: 16 }}>
        <button className="btn btn-ghost btn-sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - pageSize))}>{t('publication.previous')}</button>
        <span>{t('publication.page', { page: Math.floor(offset / pageSize) + 1 })}</span>
        <button className="btn btn-ghost btn-sm" disabled={!hasNext} onClick={() => setOffset(offset + pageSize)}>{t('publication.next')}</button>
      </div>}
    </div>
    {detail && <div ref={dialogRef} role="dialog" aria-modal="true" aria-label={t('publication.detail')} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,.65)', zIndex: 1000, display: 'flex', justifyContent: 'center', alignItems: 'center', padding: 24 }} onKeyDown={e => {
      if (e.key === 'Escape') closeDetail()
      if (e.key === 'Tab') {
        const buttons = Array.from(dialogRef.current?.querySelectorAll('button:not([disabled])') || [])
        const first = buttons[0], last = buttons[buttons.length - 1]
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last?.focus() }
        if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first?.focus() }
      }
    }}>
      <div style={{ ...panel, width: 720, maxWidth: '100%', maxHeight: '85vh', overflow: 'auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}><h2>{t('publication.detail')}</h2><button className="btn btn-ghost btn-sm" aria-label={t('publication.close')} onClick={closeDetail}><X size={16} /></button></div>
        {detailLoading ? <div role="status">{t('publication.loading')}</div> : detailError ? <div role="alert">{detailError}</div> : <>
          {detail.title && <h3>{detail.title}</h3>}
          <div style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', marginBottom: 16 }}>{detail.body}</div>
          <dl style={{ overflowWrap: 'anywhere' }}>{['platform', 'kind', 'account', 'published_at', 'recorded_at', 'remote_id', 'url', 'parent_url', 'workflow_id', 'execution_id', 'node_id', 'agent_id', 'org_id', 'role_id'].filter(key => detail[key]).map(key => <div key={key} style={{ display: 'grid', gridTemplateColumns: '130px 1fr', gap: 12, marginBottom: 8 }}><dt style={{ color: 'var(--text-muted)' }}>{t(`publication.field.${key}`)}</dt><dd style={{ margin: 0 }}>{detail[key]}</dd></div>)}</dl>
          {(detail.media || []).length > 0 && <div><strong>{t('publication.media')}</strong>{detail.media.map((media, i) => <div key={i} style={{ overflowWrap: 'anywhere' }}>{media}</div>)}</div>}
          <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
            {webURL(detail.url) && <button className="btn btn-primary btn-sm" onClick={() => api.openURL(detail.url)}><ExternalLink size={12} />{t('publication.open')}</button>}
            {webURL(detail.parent_url) && <button className="btn btn-ghost btn-sm" onClick={() => api.openURL(detail.parent_url)}>{t('publication.openParent')}</button>}
            {detail.execution_id && detail.workflow_id && detail.workflow_id !== 'cli' && onNavigate && <button className="btn btn-ghost btn-sm" onClick={() => { onNavigate('noderunner', { executionId: detail.execution_id, workflowId: detail.workflow_id }); closeDetail() }}>{t('publication.execution')}</button>}
          </div>
        </>}
      </div>
    </div>}
  </>
}
