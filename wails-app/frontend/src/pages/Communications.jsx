import { useCallback, useEffect, useRef, useState } from 'react'
import { RefreshCw, Mail } from 'lucide-react'
import { api } from '../services/api.js'
import MessageDetailModal from '../components/MessageDetailModal.jsx'
import { isUnread, UnreadDot } from '../lib/unread.jsx'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'
import { useTranslation } from 'react-i18next'
import { StatusBadge } from '../components/CommsLinks.jsx'
import i18nOr from '../lib/i18nOr.js'
import { STATUS_FILTERS, messageStatus, isPending, hilRefOf, matchesStatusFilter, onCommsChanged } from '../lib/commsStatus.js'

const FETCH_LIMIT = 200

// The status filters the CLI can apply itself: exactly one stored status.
// The grouped ones (approved+sending, failed+expired) and 'sent' (which also
// covers rows from before the field existed) stay a view of the loaded rows.
const serverStatusOf = (key) => {
  const f = STATUS_FILTERS.find(x => x.key === key)
  return f && f.statuses && f.statuses.length === 1 && f.statuses[0] !== 'sent' ? f.statuses[0] : ''
}

export default function Communications({ onProfile, isActive = true, onOpenHil, navData }) {
  const { t } = useTranslation()
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [sourceFilter, setSourceFilter] = useState('')
  const [unreadOnly, setUnreadOnly] = useState(false)
  const [statusFilter, setStatusFilter] = useState('all')
  const [openMessage, setOpenMessage] = useState(null)
  // Every source seen so far, so narrowing to one keeps the others to pick.
  const [knownSources, setKnownSources] = useState([])
  const firstLoad = useRef(true)
  // "See in Communications" (HIL page, bubbles) names the row to show.
  const focusRef = navData?.hil_ref || ''
  // "N communications" on a People row narrows the list to that person.
  const [personFilter, setPersonFilter] = useState('')
  useEffect(() => { setPersonFilter(navData?.person || '') }, [navData])

  const load = useCallback(async (opts) => {
    if (opts?.silent !== true) setLoading(true)
    try {
      setError(null)
      // The CLI applies the source, status and unread filters, so the
      // limit counts matching messages rather than the latest 200 of all.
      const data = await api.getAllPersonMessages(FETCH_LIMIT, {
        source: sourceFilter, status: serverStatusOf(statusFilter), unread: unreadOnly,
      })
      setMessages(data || [])
      setKnownSources(prev => [...new Set([...prev, ...(data || []).map(m => m.source).filter(Boolean)])])
    } catch (e) {
      setError(e?.message || i18nOr('ui6PagesMsgs.load_failed'))
    } finally {
      setLoading(false)
    }
  }, [sourceFilter, statusFilter, unreadOnly])

  // A changed filter reloads without the spinner, so the list stays in place.
  useEffect(() => {
    load({ silent: !firstLoad.current })
    firstLoad.current = false
  }, [load])
  useReloadOnActivate(isActive, () => load({ silent: true }))
  // Approving or rejecting a HIL item elsewhere changes a row here.
  useEffect(() => onCommsChanged(() => load({ silent: true })), [load])
  useEffect(() => { if (focusRef) setStatusFilter('all') }, [focusRef])

  const sources = knownSources
  const unreadCount = messages.filter(isUnread).length
  const filtered = messages
    .filter(m => !sourceFilter || m.source === sourceFilter)
    .filter(m => !unreadOnly || isUnread(m))
    .filter(m => !personFilter || m.person_id === personFilter)
    .filter(m => matchesStatusFilter(m, statusFilter))
  const hasOutbound = messages.some(m => messageStatus(m)) || statusFilter !== 'all'

  // Opening a message marks it read (the CLI owns the state; this only
  // mirrors it locally so the dot goes away at once).
  const openMsg = (msg) => {
    setOpenMessage(msg)
    if (!isUnread(msg)) return
    const now = new Date().toISOString()
    setMessages(prev => prev.map(m => (m.id === msg.id ? { ...m, read_at: now } : m)))
    // The CLI owns the state: when it refuses, put the dot back.
    Promise.resolve(api.markPersonMessagesRead('', [msg.id])).catch(() => {
      setMessages(prev => prev.map(m => (m.id === msg.id ? { ...m, read_at: msg.read_at } : m)))
    })
  }

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">{i18nOr('ui6PagesMsgs.communications')}</div>
          <div className="page-subtitle">{i18nOr('ui6PagesMsgs.across', { count: filtered.length })}</div>
        </div>
        <div className="page-header-right">
          <button className="btn btn-ghost btn-sm" onClick={load} style={{ gap: 5 }}>
            <RefreshCw size={12} /> {i18nOr('ui6PagesMsgs.refresh')}
          </button>
        </div>
      </div>

      <div className="page-body">
        {messages.length >= FETCH_LIMIT && (
          <div role="status" data-testid="comms-capped" style={{ marginBottom: 12, fontSize: 11, color: 'var(--text-muted)' }}>
            {t('commsPage.capped', { n: FETCH_LIMIT })}
          </div>
        )}
        {personFilter && (
          <div className="profile-summary-chips" style={{ marginBottom: 12 }}>
            <button type="button" className="summary-chip active" data-testid="clear-person-filter" onClick={() => setPersonFilter('')}>
              {t('comms.onePerson')} {'\u2715'}
            </button>
          </div>
        )}
        {hasOutbound && (
          <div className="profile-summary-chips" role="group" aria-label={t('comms.filterLabel')} style={{ marginBottom: 12 }}>
            {STATUS_FILTERS.map(f => (
              <button
                key={f.key}
                type="button"
                data-testid={`status-filter-${f.key}`}
                aria-pressed={statusFilter === f.key}
                className={`summary-chip ${statusFilter === f.key ? 'active' : ''}`}
                onClick={() => setStatusFilter(f.key)}
              >
                {f.key === 'all' ? t('comms.filterAll') : t(`comms.status.${f.key}`)}{' '}
                {(!serverStatusOf(statusFilter) || f.key === statusFilter) && (
                  <span>{messages.filter(m => matchesStatusFilter(m, f.key) && (f.key === 'all' || messageStatus(m))).length}</span>
                )}
              </button>
            ))}
          </div>
        )}

        {(sources.length > 1 || unreadCount > 0 || unreadOnly) && (
          <div className="profile-summary-chips" style={{ marginBottom: 12 }}>
            {(unreadCount > 0 || unreadOnly) && (
              <button className={`summary-chip ${unreadOnly ? 'active' : ''}`} onClick={() => setUnreadOnly(u => !u)}>
                {i18nOr('ui6PagesMsgs.unread')} <span>{unreadCount}</span>
              </button>
            )}
            <button
              className={`summary-chip ${!sourceFilter ? 'active' : ''}`}
              onClick={() => setSourceFilter('')}
            >
              {i18nOr('ui6PagesMsgs.all')}{!sourceFilter && <> <span>{messages.length}</span></>}
            </button>
            {sources.map(src => (
              <button
                key={src}
                className={`summary-chip ${sourceFilter === src ? 'active' : ''}`}
                onClick={() => setSourceFilter(sourceFilter === src ? '' : src)}
              >
                {src}{(!sourceFilter || sourceFilter === src) && <> <span>{messages.filter(m => m.source === src).length}</span></>}
              </button>
            ))}
          </div>
        )}

        {loading ? (
          <div className="empty-state" style={{ height: '50vh' }}>
            <div className="spinner" />
          </div>
        ) : error ? (
          <div style={{ padding: '12px 16px', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)' }}>
            {error}
          </div>
        ) : filtered.length === 0 ? (
          <div className="empty-state" style={{ height: '50vh' }}>
            <Mail size={32} style={{ opacity: 0.3, marginBottom: 8 }} />
            <div className="empty-state-title">{i18nOr('ui6PagesMsgs.empty')}</div>
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {filtered.map(msg => (
              <div
                key={msg.id}
                data-focused={focusRef && hilRefOf(msg) === focusRef ? 'true' : undefined}
                role="button"
                tabIndex={0}
                onClick={() => openMsg(msg)}
                onKeyDown={e => { if (e.key === 'Enter') openMsg(msg) }}
                style={{
                  display: 'flex', flexDirection: 'column', gap: 4, textAlign: 'left',
                  padding: '10px 12px', borderRadius: 6, cursor: 'pointer',
                  background: 'var(--elevated)', border: '1px solid var(--border)',
                  width: '100%', font: 'inherit', color: 'inherit',
                  ...(focusRef && hilRefOf(msg) === focusRef ? { outline: '2px solid var(--cyan)' } : {}),
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  {isUnread(msg) && <UnreadDot />}
                  <span style={{
                    padding: '1px 6px', borderRadius: 4,
                    background: 'rgba(0,180,216,0.12)',
                    border: '1px solid rgba(0,180,216,0.3)',
                    color: '#00b4d8', fontSize: 9,
                    fontFamily: 'var(--font-mono)', flexShrink: 0,
                  }}>
                    {msg.source}
                  </span>
                  <StatusBadge status={messageStatus(msg)} />
                  <button
                    onClick={e => { e.stopPropagation(); onProfile?.(msg.person_id) }}
                    style={{
                      fontFamily: 'var(--font-mono)', fontSize: 11.5,
                      color: 'var(--text)', fontWeight: 600, flexShrink: 0,
                      background: 'none', border: 'none', cursor: 'pointer', padding: 0,
                    }}
                  >
                    {msg.person_full_name || msg.person_platform_username}
                  </button>
                  <span style={{
                    fontFamily: 'var(--font-mono)', fontSize: 11,
                    color: 'var(--text-muted)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }}>
                    {msg.subject || i18nOr('ui6PagesMsgs.no_subject')}
                  </span>
                  <span style={{
                    marginLeft: 'auto', flexShrink: 0,
                    fontFamily: 'var(--font-mono)', fontSize: 10,
                    color: 'var(--text-dim)',
                  }}>
                    {(msg.sent_at || msg.created_at || '').slice(0, 16).replace('T', ' ')}
                  </span>
                </div>
                {isPending(msg) && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 10, fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#f59e0b' }}>
                    <span>{t('comms.waiting')}</span>
                    {onOpenHil && (
                      <button
                        type="button"
                        className="btn btn-primary btn-sm"
                        data-testid="comms-review"
                        onClick={e => { e.stopPropagation(); onOpenHil(hilRefOf(msg)) }}
                      >
                        {t('comms.review')}
                      </button>
                    )}
                  </div>
                )}
                {msg.body && (
                  <div style={{
                    fontFamily: 'var(--font-mono)', fontSize: 10.5,
                    color: 'var(--text-muted)',
                    overflow: 'hidden', textOverflow: 'ellipsis',
                    display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical',
                  }}>
                    {stripHTML(msg.body)}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      {openMessage && (
        <MessageDetailModal
          message={openMessage}
          personLabel={openMessage.person_full_name || openMessage.person_platform_username}
          onClose={() => setOpenMessage(null)}
        />
      )}
    </>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
// rendered properly in MessageDetailModal via a sandboxed iframe.
function stripHTML(body) {
  return body.replace(/<style[\s\S]*?<\/style>/gi, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
}
