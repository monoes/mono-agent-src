import { useEffect, useRef, useState } from 'react'
import { Sparkles } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { api, onMonomindInitEvent, notify } from '../services/api.js'
import i18nOr from '../lib/i18nOr.js'

const MAX_LOG_LINES = 10

/**
 * MonomindInitPrompt — shown in place of the normal empty state on the Orgs
 * and Agents tabs when the active profile's folder has never been set up
 * with `monomind init` (see api.isMonomindInitialized / App.IsMonomindInitialized
 * in wails-app/app_monomind_init.go). One button, streamed progress, calls
 * onInitialized() when done so the parent can re-fetch its data.
 *
 * @param {() => void} onInitialized
 */
export default function MonomindInitPrompt({ onInitialized }) {
  const { t } = useTranslation()
  const [status, setStatus] = useState('idle') // idle | running | error
  const [lines, setLines] = useState([])
  const runningRef = useRef(false)

  useEffect(() => onMonomindInitEvent((payload) => {
    if (!runningRef.current) return
    if (payload.kind === 'line') {
      setLines(prev => [...prev, payload.message].slice(-MAX_LOG_LINES))
    } else if (payload.kind === 'error') {
      runningRef.current = false
      setStatus('error')
      notify(i18nOr('ui6Set.op_init_monomind'), payload.message || i18nOr('ui6Set.failed_word'))
    } else if (payload.kind === 'done') {
      runningRef.current = false
      setStatus('idle')
      onInitialized?.()
    }
  }), [onInitialized])

  const start = async () => {
    runningRef.current = true
    setStatus('running')
    setLines([])
    // The binding returns before init finishes; progress arrives as events.
    // A refusal (e.g. signed out) is returned without an event, so read it
    // here or the spinner would never stop.
    let message = ''
    try {
      const r = await api.initializeMonomindProfile()
      if (r && r.error) message = r.login_required ? t('monomindInit.signInAgain', { error: r.error }) : r.error
    } catch (e) {
      message = e?.message || String(e)
    }
    if (message && runningRef.current) {
      runningRef.current = false
      setStatus('error')
      notify(i18nOr('ui6Set.op_init_monomind'), message)
    }
  }

  return (
    <div className="empty-state" style={{ flex: 1 }}>
      <div className="empty-state-icon"><Sparkles size={30} /></div>
      <div className="empty-state-title">
        {status === 'running' ? t('monomindInit.titleRunning') : t('monomindInit.title')}
      </div>
      <div className="empty-state-desc">
        {status === 'running'
          ? t('monomindInit.descRunning')
          : t('monomindInit.desc')}
      </div>
      {status === 'running' ? (
        <>
          <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>
          {lines.length > 0 && (
            <div style={{
              fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)',
              textAlign: 'left', maxWidth: 440, margin: '8px auto 0', maxHeight: 140, overflowY: 'auto',
            }}>
              {lines.map((l, i) => <div key={i} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{l}</div>)}
            </div>
          )}
        </>
      ) : (
        <button className="btn btn-primary btn-sm" onClick={start} style={{ marginTop: 8 }}>
          {t('monomindInit.start')}
        </button>
      )}
    </div>
  )
}
