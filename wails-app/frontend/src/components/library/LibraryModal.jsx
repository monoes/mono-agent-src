// Browse the monoes.me library for one kind (workflow | automation | org)
// and add items to this machine. Every library read needs a monoes.me
// login, official items included, so a logged-out person first sees the
// login gate; once logged in: Official · Community · Mine. An expired
// session (a "log in first" answer while browsing) returns to the gate.
// All facts (what is installed, whether an update exists, name collisions)
// come from `monoagentcli library …`; this dialog only shows them.
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X, Search, Library } from 'lucide-react'
import { library, isLoginRequired, LIBRARY_TABS } from '../../services/library.js'
import LogInToMonoesButton from './LogInToMonoesButton.jsx'
import LibraryItemCard from './LibraryItemCard.jsx'
import { useEscapeClose } from '../../lib/escapeStack.js'

const mono = { fontFamily: 'var(--font-mono)', fontSize: 11 }
const PER_PAGE = 20

// statusFrom reads `library status`: its own error field (an expired saved
// login) keeps logged_in; a failed call counts as logged out.
const statusFrom = (s) => (typeof s?.logged_in === 'boolean' ? s : { logged_in: false, error: s?.error || '' })

function LoginGate({ kind, status, note, onStatusChange }) {
  const { t } = useTranslation()
  return (
    <div role="region" aria-labelledby="library-gate-title"
      style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center', gap: 12, padding: '28px 24px 24px' }}>
      <Library size={28} style={{ color: 'var(--cyan)' }} aria-hidden="true" />
      <div id="library-gate-title" style={{ fontFamily: 'var(--font-body)', fontSize: 15, fontWeight: 600, color: 'var(--text)' }}>
        {t('library.gate.title')}
      </div>
      <div style={{ fontFamily: 'var(--font-body)', fontSize: 12.5, lineHeight: 1.5, color: 'var(--text-secondary)', maxWidth: 440 }}>
        {t(`library.gate.body.${kind}`)}
      </div>
      {note && (
        <div role="status" style={{ ...mono, color: 'var(--yellow)', maxWidth: 440 }}>{note}</div>
      )}
      <LogInToMonoesButton status={status} onStatusChange={onStatusChange} large />
    </div>
  )
}

export default function LibraryModal({ kind, onClose, onInstalled }) {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [expired, setExpired] = useState(false) // the gate is back because the session expired
  const [tab, setTab] = useState('official')
  const [query, setQuery] = useState('')
  const [search, setSearch] = useState('')
  const [items, setItems] = useState([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)

  useEffect(() => {
    let live = true
    library.status(false).then(s => {
      if (!live) return
      const st = statusFrom(s)
      setStatus(st)
      if (!st.logged_in && st.error) setExpired(true)
    })
    return () => { live = false }
  }, [])

  // Debounce typing into the search box.
  useEffect(() => {
    const id = setTimeout(() => setSearch(query.trim()), 300)
    return () => clearTimeout(id)
  }, [query])

  const loggedIn = status?.logged_in === true

  // The CLI said "log in first" while browsing: the session expired and
  // could not be refreshed. Back to the gate, saying why.
  const sessionExpired = useCallback(() => {
    setStatus(s => ({ ...(s || {}), logged_in: false, user: null }))
    setExpired(true)
    setItems([]); setTotal(0); setError('')
  }, [])

  const onGateStatus = (s) => {
    setStatus(s)
    if (s?.logged_in) setExpired(false)
  }

  useEffect(() => {
    if (!loggedIn) return
    let live = true
    setLoading(true); setError('')
    const scope = LIBRARY_TABS.find(x => x.id === tab).scope
    library.list(kind, scope, search, 1).then(res => {
      if (!live) return
      setLoading(false)
      if (isLoginRequired(res)) { sessionExpired(); return }
      if (res?.error) { setError(res.error); setItems([]); setTotal(0); return }
      let list = Array.isArray(res?.items) ? res.items : []
      if (tab === 'community') list = list.filter(i => i.visibility !== 'official')
      setItems(list); setTotal(res.total || list.length); setPage(1)
    })
    return () => { live = false }
  }, [kind, tab, search, loggedIn, reload, sessionExpired])

  const loadMore = useCallback(async () => {
    const scope = LIBRARY_TABS.find(x => x.id === tab).scope
    const res = await library.list(kind, scope, search, page + 1)
    if (isLoginRequired(res)) { sessionExpired(); return }
    if (res?.error) { setError(res.error); return }
    let more = Array.isArray(res?.items) ? res.items : []
    if (tab === 'community') more = more.filter(i => i.visibility !== 'official')
    setItems(prev => [...prev, ...more]); setPage(page + 1)
  }, [kind, tab, search, page, sessionExpired])

  useEscapeClose(onClose)

  const installed = (item, res) => {
    // Show the new installed state on the card without a round trip.
    setItems(prev => prev.map(i => i.id === item.id
      ? { ...i, installed: { local_id: res.local_id, version: item.version, update_available: false } }
      : i))
    onInstalled?.(res)
  }

  return (
    <div className="modal-overlay" style={{ zIndex: 1100 }} onMouseDown={e => { if (e.target === e.currentTarget) onClose?.() }}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="library-modal-title"
        style={{ width: 640, display: 'flex', flexDirection: 'column', gap: 12, padding: 20 }}>
        <div className="modal-title" style={{ marginBottom: 0 }}>
          <span id="library-modal-title">{t(`library.title.${kind}`)}</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label={t('library.close')}><X size={15} /></button>
        </div>

        {!status ? (
          <div style={{ ...mono, color: 'var(--text-muted)', padding: 24, textAlign: 'center' }}>{t('library.checking')}</div>
        ) : !loggedIn ? (
          <LoginGate kind={kind} status={status} note={expired ? t('library.gate.expired') : ''} onStatusChange={onGateStatus} />
        ) : (
          <>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, flexWrap: 'wrap' }}>
              <div role="tablist" style={{ display: 'flex', gap: 4 }}>
                {LIBRARY_TABS.map(x => (
                  <button key={x.id} role="tab" aria-selected={tab === x.id} onClick={() => setTab(x.id)}
                    style={{
                      ...mono, fontSize: 10, fontWeight: 700, padding: '5px 10px', borderRadius: 'var(--radius)', cursor: 'pointer',
                      background: tab === x.id ? 'var(--cyan-glow-strong)' : 'transparent',
                      border: tab === x.id ? '1px solid var(--border-active)' : '1px solid transparent',
                      color: tab === x.id ? 'var(--cyan)' : 'var(--text-muted)',
                    }}>
                    {t(`library.tabs.${x.id}`)}
                  </button>
                ))}
              </div>
              <LogInToMonoesButton status={status} onStatusChange={setStatus} compact />
            </div>

            <label style={{ position: 'relative', display: 'block' }}>
              <Search size={12} style={{ position: 'absolute', left: 10, top: 11, color: 'var(--text-muted)' }} />
              <input className="form-input" style={{ paddingLeft: 30 }} value={query} onChange={e => setQuery(e.target.value)}
                placeholder={t('library.searchPlaceholder')} aria-label={t('library.searchPlaceholder')} />
            </label>

            <div style={{ minHeight: 160, maxHeight: '52vh', overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
              {loading ? (
                <div style={{ ...mono, color: 'var(--text-muted)', padding: 24, textAlign: 'center' }}>{t('library.loading')}</div>
              ) : error ? (
                <div role="alert" style={{ ...mono, color: 'var(--red)', padding: 16, display: 'flex', gap: 10, alignItems: 'center', justifyContent: 'center' }}>
                  <span>{error}</span>
                  <button className="btn btn-ghost btn-sm" onClick={() => setReload(n => n + 1)}>{t('library.retry')}</button>
                </div>
              ) : items.length === 0 ? (
                <div style={{ ...mono, color: 'var(--text-muted)', padding: 24, textAlign: 'center' }}>
                  {tab === 'mine' ? t('library.emptyMine') : t('library.empty')}
                </div>
              ) : (
                <>
                  {items.map(item => (
                    <LibraryItemCard key={item.id} item={item} kind={kind}
                      onInstalled={res => installed(item, res)} onLoginRequired={sessionExpired} />
                  ))}
                  {total > page * PER_PAGE && (
                    <button className="btn btn-ghost btn-sm" style={{ alignSelf: 'center' }} onClick={loadMore}>{t('library.loadMore')}</button>
                  )}
                </>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
