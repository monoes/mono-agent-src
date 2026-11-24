import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Terminal, Trash2, ArrowDown } from 'lucide-react'
import RefreshButton from '../components/RefreshButton.jsx'
import SendToCoderButton from '../components/SendToCoderButton.jsx'
import { selectionText } from '../components/SendToCoderApi.js'

const LEVEL_COLORS = {
  INFO:   { level: 'log-level-info',   msg: 'log-msg-info' },
  WARN:   { level: 'log-level-warn',   msg: 'log-msg-warn' },
  WARNING:{ level: 'log-level-warn',   msg: 'log-msg-warn' },
  ERROR:  { level: 'log-level-error',  msg: 'log-msg-error' },
  SYSTEM: { level: 'log-level-system', msg: 'log-msg-system' },
}

export default function Logs({ logs, onClear, onRefresh }) {
  const { t } = useTranslation()
  const endRef = useRef(null)
  const containerRef = useRef(null)
  const [autoScroll, setAutoScroll] = useState(true)
  const [filter, setFilter] = useState('')
  const [levelFilter, setLevelFilter] = useState('all')
  const [refreshing, setRefreshing] = useState(false)

  const handleRefresh = async () => {
    setRefreshing(true)
    try { await onRefresh?.() } finally { setRefreshing(false) }
  }

  useEffect(() => {
    if (autoScroll && endRef.current) {
      endRef.current.scrollIntoView({ behavior: 'smooth' })
    }
  }, [logs, autoScroll])

  const handleScroll = () => {
    if (!containerRef.current) return
    const { scrollTop, scrollHeight, clientHeight } = containerRef.current
    const nearBottom = scrollHeight - scrollTop - clientHeight < 80
    setAutoScroll(nearBottom)
  }

  // Keep each line's position in the full list: it is the row key, so
  // filtering does not remount the rows that stay visible.
  const filtered = logs.map((l, pos) => ({ l, pos })).filter(({ l }) => {
    if (levelFilter !== 'all') {
      const lvl = (l.level || '').toUpperCase()
      if (lvl !== levelFilter && !(levelFilter === 'WARN' && lvl === 'WARNING')) return false
    }
    if (filter) {
      const q = filter.toLowerCase()
      if (
        !l.message?.toLowerCase().includes(q) &&
        !l.source?.toLowerCase().includes(q) &&
        !l.level?.toLowerCase().includes(q)
      ) return false
    }
    return true
  }).map(({ l, pos }) => ({ ...l, _pos: pos }))

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">Live Logs</div>
          <div className="page-subtitle">Real-time Execution</div>
        </div>
        <div className="page-header-right">
          <RefreshButton onClick={handleRefresh} loading={refreshing} />
          {!autoScroll && (
            <button
              className="btn btn-secondary btn-sm"
              onClick={() => {
                setAutoScroll(true)
                endRef.current?.scrollIntoView({ behavior: 'smooth' })
              }}
              style={{ gap: 5, animation: 'pulse-dot 1.5s infinite' }}
            >
              <ArrowDown size={12} /> Scroll to Bottom
            </button>
          )}
          <button className="btn btn-danger btn-sm" onClick={onClear} style={{ gap: 5 }}>
            <Trash2 size={12} /> Clear
          </button>
        </div>
      </div>

      <div className="page-body" style={{ display: 'flex', flexDirection: 'column', gap: 12, overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <input
            className="search-input"
            placeholder="Filter logs..."
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ maxWidth: 300 }}
          />
          <select
            className="filter-select"
            value={levelFilter}
            onChange={e => setLevelFilter(e.target.value)}
            style={{ maxWidth: 120 }}
          >
            <option value="all">All Levels</option>
            <option value="INFO">INFO</option>
            <option value="WARN">WARN</option>
            <option value="ERROR">ERROR</option>
            <option value="SYSTEM">SYSTEM</option>
          </select>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>
            {filtered.length} {filtered.length !== 1 ? 'entries' : 'entry'}
          </span>
          {autoScroll && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginLeft: 'auto' }}>
              <span className="live-dot" />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--teal)' }}>{t('logsPage.live')}</span>
            </div>
          )}
        </div>

        {filtered.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state-icon"><Terminal size={40} /></div>
            <div className="empty-state-title">No Logs</div>
            <div className="empty-state-desc">
              Logs will appear here when actions are running.
            </div>
          </div>
        ) : (
          <div
            ref={containerRef}
            onScroll={handleScroll}
            role="log"
            aria-label={t('logsPage.log')}
            className="log-terminal"
            style={{ flex: 1, minHeight: 0 }}
          >
            {filtered.map(entry => {
              const colors = LEVEL_COLORS[entry.level?.toUpperCase()] || LEVEL_COLORS.INFO
              const key = `${entry.time}-${entry.source}-${entry._pos}`
              return (
                <div key={key} className="log-line">
                  <span className="log-time">{entry.time}</span>
                  <span className={`log-source ${colors.level}`}>[{(entry.source || 'SYS').slice(0, 6)}]</span>
                  <span className={colors.msg}>{entry.message}</span>
                  {entry.level?.toUpperCase() === 'ERROR' && (
                    <SendToCoderButton label="Ask coder about this" goal="Explain this error from the app's log and what to do about it."
                      context={[selectionText(`[${entry.source || 'SYS'}] ${entry.message}`)]} origin={{ page: 'logs', label: 'Logs' }}
                      className="btn btn-ghost btn-sm" style={{ marginLeft: 8 }} />
                  )}
                </div>
              )
            })}
            <div ref={endRef} />
          </div>
        )}
      </div>
    </>
  )
}
