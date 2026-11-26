import { useState, useEffect } from 'react'
import { useMountedTimers } from '../lib/useMountedTimers'
import { useTranslation } from 'react-i18next'
import { History, UserCheck } from 'lucide-react'
import { getHealth, subscribeHealth, summarize } from '../lib/health.js'
import { GetVersion, CheckForUpdate, AppSelfUpdate, QuitApp } from '../wailsjs/go/main/App'
import { subscribeEvent } from '../services/api.js'
import { toggleHistory } from './history/historyBus.js'
import UpdateNotice from './UpdateNotice.jsx'
import i18nOr from '../lib/i18nOr.js'

// HealthDot: the latest System health verdict (the background check runs on
// start and every 30 minutes, see lib/health.js); click opens Settings.
function HealthDot({ onOpen }) {
  const { t } = useTranslation()
  const [h, setH] = useState(getHealth())
  useEffect(() => subscribeHealth(setH), [])
  if (!h.report && !h.cliMissing) return null
  const sum = summarize(h.report)
  const level = h.cliMissing ? 'broken' : sum.level
  const color = { ok: 'var(--green-neon)', issues: '#fbbf24', broken: 'var(--red)' }[level]
  const label = h.cliMissing ? t('settings.health.cliMissingTitle')
    : level === 'broken' ? t('settings.health.statusBroken')
      : level === 'issues' ? t('settings.health.statusIssues', { n: sum.issues })
        : t('settings.health.sectionTitle')
  return (
    <button
      onClick={onOpen}
      // The accessible name carries the visible status too (#146).
      title={`${label} — ${t('settings.health.statusTitle')}`}
      aria-label={`${label} — ${t('settings.health.statusTitle')}`}
      data-testid="health-dot"
      style={{ display: 'inline-flex', alignItems: 'center', gap: 5, background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 10, padding: 0 }}
    >
      <span style={{ width: 6, height: 6, borderRadius: '50%', background: color, boxShadow: `0 0 4px ${color}` }} />
      {label}
    </button>
  )
}

export default function StatusBar({ stats, dbConnected, hilOpen, hilCount = 0, onToggleHil, onOpenHealth }) {
  const timers = useMountedTimers()
  const { t } = useTranslation()
  const running = stats?.executions_by_status?.RUNNING || 0
  const total   = stats?.total_workflows || 0
  const people  = stats?.total_people || 0
  const sessions = stats?.active_sessions || 0

  const [ver, setVer] = useState(null)
  const [update, setUpdate] = useState(null)   // { checking, available, latest, error, updating, progress }

  useEffect(() => {
    GetVersion().then(v => setVer(v)).catch(() => {})
  }, [])

  // Listen for update progress events
  useEffect(() => {
    const off = subscribeEvent('update:progress', msg => {
      setUpdate(u => ({ ...u, progress: msg }))
    })
    return off
  }, [])

  function handleVersionClick() {
    if (update?.updating) return
    setUpdate({ checking: true })
    CheckForUpdate()
      .then(info => {
        if (info.error) {
          setUpdate({ error: info.error })
        } else if (info.update_available) {
          setUpdate({ available: true, latest: info.latest_version, url: info.release_url })
        } else {
          setUpdate({ upToDate: true })
          timers.timeout(() => setUpdate(null), 3000)
        }
      })
      .catch(e => setUpdate({ error: String(e) }))
  }

  function handleUpdate() {
    // AppSelfUpdate replaces the running GUI app binary itself and
    // restarts it -- SelfUpdate (used here previously) only replaces the
    // monoagentcli CLI sidecar, which is why clicking "Update" reported
    // success and a new version number but the app's own displayed
    // version (versionText below, from GetVersion()) never changed even
    // after a manual restart: the actual running executable was never
    // touched. Settings.jsx already calls AppSelfUpdate correctly; this
    // was the one place still calling the wrong function.
    setUpdate(u => ({ ...u, updating: true, progress: t('ui6Set.update_starting') }))
    AppSelfUpdate()
      .then(result => {
        if (result.success) {
          setUpdate({ done: true, latest: result.new_version, notice: result.notice, noticeOpen: !!result.notice })
        } else {
          setUpdate({ error: result.error })
        }
      })
      .catch(e => setUpdate({ error: String(e) }))
  }

  const versionText = ver ? `v${ver.version.replace(/^v/, '')}` : 'v…'

  return (
    <div className="status-bar">
      <div className="status-bar-item">
        <span className={`status-dot ${dbConnected ? 'connected' : 'disconnected'}`}
              style={{ width: 5, height: 5 }} />
        <span>{dbConnected ? t('ui6Set.status_connected') : t('ui6Set.status_offline')}</span>
      </div>
      <div className="status-bar-item">
        {t('ui6Set.status_workflows')} <span>{total}</span>
      </div>
      {running > 0 && (
        <div className="status-bar-item" style={{ color: 'var(--teal)' }}>
          <span className="live-dot" style={{ width: 5, height: 5 }} />
          <span style={{ color: 'inherit' }}>{t('ui6Set.status_running', { n: running })}</span>
        </div>
      )}
      <div className="status-bar-item">
        {t('ui6Set.status_people')} <span>{people}</span>
      </div>
      <div className="status-bar-item">
        {t('ui6Set.status_sessions')} <span>{sessions}</span>
      </div>

      {/* Version + update indicator */}
      <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8, fontSize: 10, position: 'relative' }}>
        <HealthDot onOpen={onOpenHealth} />
        {/* Update status popover */}
        {update && (
          <div style={{
            fontFamily: 'var(--font-mono)',
            fontSize: 10,
            display: 'flex',
            alignItems: 'center',
            gap: 6,
          }}>
            {update.checking && (
              <span style={{ color: 'var(--text-muted)' }}>{t('ui6Set.update_checking')}</span>
            )}
            {update.upToDate && (
              <span style={{ color: '#00f5d4' }}>{t('ui6Set.update_current')}</span>
            )}
            {update.error && (
              <span style={{ color: 'var(--red)', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                    title={update.error}>
                {t('ui6Set.update_failed')}
              </span>
            )}
            {update.available && !update.updating && !update.done && (
              <>
                <span style={{ color: '#fbbf24' }}>{t('ui6Set.update_available', { latest: update.latest })}</span>
                <button
                  onClick={handleUpdate}
                  style={{
                    background: '#00b4d8',
                    color: '#fff',
                    border: 'none',
                    borderRadius: 3,
                    padding: '2px 8px',
                    cursor: 'pointer',
                    fontFamily: 'var(--font-mono)',
                    fontSize: 9,
                  }}
                >
                  {t('ui6Set.status_update')}
                </button>
                <button
                  onClick={() => setUpdate(null)}
                  style={{
                    background: 'transparent',
                    color: 'var(--text-muted)',
                    border: 'none',
                    cursor: 'pointer',
                    fontSize: 10,
                    padding: '0 2px',
                  }}
                >✕</button>
              </>
            )}
            {update.updating && (
              <span style={{ color: '#00b4d8' }}>{update.progress || t('ui6Set.update_running')}</span>
            )}
            {update.done && (
              <span style={{ color: update.notice ? '#fbbf24' : '#00f5d4' }}>
                {i18nOr('ui6Set.updated_to', { latest: update.latest })}
              </span>
            )}
          </div>
        )}

        {update?.done && update.noticeOpen && (
          <UpdateNotice
            notice={update.notice}
            onRestart={() => QuitApp()}
            onLater={() => setUpdate(u => ({ ...u, noticeOpen: false }))}
            style={{ position: 'absolute', right: 8, bottom: '100%', marginBottom: 4, zIndex: 50 }}
          />
        )}
        <span
          onClick={handleVersionClick}
          title={t('ui6Set.update_click')}
          style={{
            color: 'var(--text-dim)',
            cursor: 'pointer',
            userSelect: 'none',
            transition: 'color .15s',
          }}
          onMouseEnter={e => e.currentTarget.style.color = '#00b4d8'}
          onMouseLeave={e => e.currentTarget.style.color = 'var(--text-dim)'}
        >
          MonoAgent · {versionText}
        </span>

        {/* Human in Loop toggle — beside the chat icon */}
        {onToggleHil && (
          <button
            onClick={onToggleHil}
            title={hilOpen ? t('ui6Set.hil_close') : (hilCount > 0 ? t('ui6Set.hil_pending', { count: hilCount }) : t('ui6Set.hil_reviews'))}
            aria-label={hilOpen ? t('ui6Set.hil_close') : t('ui6Set.hil_open')}
            data-testid="hil-status-toggle"
            style={{
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              position: 'relative',
              width: 18, height: 18, padding: 0,
              background: 'transparent',
              border: 'none',
              borderRadius: 3,
              color: hilOpen ? '#00b4d8' : (hilCount > 0 ? '#ef4444' : 'var(--text-dim)'),
              cursor: 'pointer',
              transition: 'color .15s',
            }}
            onMouseEnter={e => { if (!hilOpen && hilCount === 0) e.currentTarget.style.color = '#00b4d8' }}
            onMouseLeave={e => { if (!hilOpen && hilCount === 0) e.currentTarget.style.color = 'var(--text-dim)' }}
          >
            <UserCheck size={12} />
            {hilCount > 0 && (
              <span style={{
                position: 'absolute', top: -5, right: -5,
                minWidth: 12, height: 12, padding: '0 2px', borderRadius: 6,
                background: 'var(--red, #ef4444)', color: '#fff',
                fontFamily: 'var(--font-mono)', fontSize: 8, fontWeight: 700,
                lineHeight: '12px', textAlign: 'center',
                boxShadow: '0 0 0 1.5px #060b13',
              }}>
                {hilCount > 99 ? '99+' : hilCount}
              </span>
            )}
          </button>
        )}

        {/* Chat history and old sessions: small icon at the far right of the
            bar, where the chat icon was, reachable from anywhere in the app.
            A new chat starts from the dock's + button or the drawer. */}
        <button
          onClick={() => toggleHistory()}
          title={t('history.title')}
          aria-label={t('history.title')}
          aria-haspopup="dialog"
          data-testid="history-status-toggle"
          style={{
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            width: 18, height: 18, padding: 0,
            background: 'transparent',
            border: 'none',
            borderRadius: 3,
            color: 'var(--text-dim)',
            cursor: 'pointer',
            transition: 'color .15s',
          }}
          onMouseEnter={e => { e.currentTarget.style.color = '#00b4d8' }}
          onMouseLeave={e => { e.currentTarget.style.color = 'var(--text-dim)' }}
        >
          <History size={12} />
        </button>
      </div>
    </div>
  )
}
