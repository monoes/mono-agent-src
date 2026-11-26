import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Square } from 'lucide-react'
import { api } from '../../services/api.js'
import { vaultRelated } from './toolLinks.js'
import { maskCredentials } from './credentials.js'
import i18nOr from '../../lib/i18nOr.js'

// The warning a coder turn leaves when it ends with background processes
// still running (notice coder.background, #203), with "Stop all": `coder
// stop-background` stops only the pids that turn reported, and only while
// they still belong to it, then says what happened to each. Each process is
// listed with its command: many are the folder's own setup daemons (e.g.
// `monomind ui`), not the agent's work.

const mono = 'var(--font-mono)'
const AMBER = '#fbbf24'

// stopSummary turns {stopped, gone, refused} into one line.
export function stopSummary(res) {
  const stopped = res?.stopped?.length || 0
  const gone = res?.gone?.length || 0
  const refused = res?.refused?.length || 0
  const parts = []
  if (stopped) parts.push(i18nOr('ui7Chat.bg_stopped', { count: stopped }))
  if (gone) parts.push(i18nOr('ui7Chat.bg_gone', { count: gone }))
  if (refused) parts.push(i18nOr('ui7Chat.bg_refused', { count: refused, pids: res.refused.join(', ') }))
  return parts.length ? parts.join(' · ') : i18nOr('ui7Chat.bg_nothing')
}

// shownCommand is a process's command line as the banner shows it: one that
// may touch the vault says only that, and a credential in it reads ••• (#872).
function shownCommand(command, t) {
  return vaultRelated(command) ? t('toolLinks.vaultRelated') : maskCredentials(command)
}

export function CoderBackgroundBanner({ notice }) {
  const { t } = useTranslation()
  const [state, setState] = useState(null) // { busy } | { result } | { error }
  const canStop = !!notice.conversationId && !!notice.turnId
  const stopAll = async () => {
    setState({ busy: true })
    try {
      setState({ result: await api.coderStopBackground(notice.conversationId, notice.turnId) })
    } catch (e) {
      setState({ error: String(e?.message || e) })
    }
  }
  const done = !!state?.result
  const processes = notice.processes || []
  const n = processes.length
  return (
    <div data-testid="coder-background-banner" style={{
      display: 'flex', flexDirection: 'column', gap: 5, padding: '6px 8px', marginTop: 6, borderRadius: 6,
      background: done ? 'rgba(148,163,184,0.06)' : `${AMBER}14`, border: `1px solid ${done ? 'rgba(148,163,184,0.25)' : `${AMBER}40`}`,
    }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 6 }}>
        <AlertTriangle size={11} color={done ? '#94a3b8' : AMBER} style={{ marginTop: 1, flexShrink: 0 }} />
        <span style={{ flex: 1, fontFamily: mono, fontSize: 10, color: done ? '#94a3b8' : AMBER }}>
          {n > 0 ? i18nOr('ui7Chat.bg_running', { count: n }) : notice.message}
        </span>
        {canStop && !done && (
          <button type="button" onClick={stopAll} disabled={!!state?.busy} style={{
            display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0, cursor: state?.busy ? 'default' : 'pointer',
            background: 'rgba(239,68,68,0.12)', border: '1px solid rgba(239,68,68,0.35)', borderRadius: 5,
            padding: '2px 7px', color: '#ef4444', fontFamily: mono, fontSize: 9.5,
          }}>
            <Square size={8} fill="#ef4444" /> {i18nOr(state?.busy ? 'ui7Chat.bg_stopping' : 'ui7Chat.bg_stop_all')}
          </button>
        )}
      </div>
      {n > 0 && (
        <ul data-testid="coder-background-processes" style={{ margin: 0, padding: '0 0 0 17px', listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 2 }}>
          {processes.map(p => (
            <li key={p.pid} style={{ display: 'flex', gap: 8, fontFamily: mono, fontSize: 9.5, minWidth: 0 }}>
              <span style={{ color: 'var(--text-muted)', flexShrink: 0 }}>{p.pid}</span>
              <span title={shownCommand(p.command, t)} style={{ color: '#e2e8f0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{shownCommand(p.command, t) || i18nOr('ui7Chat.bg_unknown_command')}</span>
            </li>
          ))}
        </ul>
      )}
      {done && <div data-testid="coder-background-result" role="status" style={{ fontFamily: mono, fontSize: 9.5, color: '#e2e8f0', paddingLeft: 17 }}>{stopSummary(state.result)}</div>}
      {state?.error && <div role="alert" style={{ fontFamily: mono, fontSize: 9.5, color: '#fca5a5', paddingLeft: 17 }}>{i18nOr('ui7Chat.bg_stop_failed')} {state.error}</div>}
    </div>
  )
}
