import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { OctagonX } from 'lucide-react'
import { controlRoomApi } from '../../services/controlRoom.js'
import { stopLine, transcriptMessage, isRepair, isWarnLine, outcome } from './stopOutcome.js'

const mono = 'var(--font-mono)'

// The Stop of an open-ended run, in the chat header. It is the control room's Stop (the same binding, the same two steps,
// and it works signed out): the CLI does the stopping, ends the run's sessions and writes a message into the transcript,
// and this shows what the command said without softening it. Offered when the chat is open-ended and the profile's run is
// not known to be gone, or whenever the probe says a run exists; a normal chat with no run never shows it.
export function showStop(mode, runExists) {
  return runExists === true || (mode === 'open' && runExists !== false)
}

export { stopLine, transcriptMessage, isRepair, isWarnLine, outcome }

export function OpenEndedStop({ chatMode, runExists, conversationId, onStopped, api = controlRoomApi }) {
  const { t } = useTranslation()
  const [phase, setPhase] = useState('idle') // idle | confirm | stopping | done
  const [result, setResult] = useState(null)
  const box = useRef(null)
  const stopBtn = useRef(null)
  const cancelBtn = useRef(null)
  const was = useRef('idle')
  // Focus moves into the confirm dialog (on its safe button) and comes back to the Stop button, or the stable wrapper when
  // that button is gone, once the dialog closes.
  useEffect(() => {
    if (phase === 'confirm') cancelBtn.current?.focus()
    else if (was.current === 'confirm') (stopBtn.current || box.current)?.focus()
    was.current = phase
  }, [phase])

  const goneNow = phase === 'done' && outcome(result) === 'gone'
  const offered = showStop(chatMode?.state?.mode, runExists) && !goneNow
  // The result stays until dismissed, even when the probe has caught up and says there is no run any more.
  if (!offered && !result && phase === 'idle') return null

  const confirmStop = async () => {
    setPhase('stopping')
    const r = await api.stop()
    setResult(r)
    setPhase('done')
    const kind = outcome(r)
    if (kind === 'gone') { if (onStopped) onStopped(''); return } // already stopped: refresh the header, no transcript message
    if (kind === 'ok' || (kind === 'warn' && /^stopped/.test(String(r?.output || '').trim())) ) {
      if (onStopped) {
        const saved = transcriptMessage(r.output, conversationId)
        // When the CLI wrote no message for this chat the one shown here is only local, and says so.
        onStopped(saved || t('chatMode.stop.localMessage', { message: t('chatMode.stop.message', { time: new Date().toLocaleString(), line: stopLine(r.output) }) }))
      }
    }
  }
  const kind = phase === 'done' ? outcome(result) : ''
  const stoppedNow = kind === 'ok' // the probe has not caught up yet while the button is still offered
  const dialogKey = e => { if (e.key === 'Escape') { e.stopPropagation(); setPhase(result ? 'done' : 'idle') } }
  const dismiss = () => { setResult(null); setPhase('idle') }
  return (
    <span ref={box} tabIndex={-1} data-testid="chat-stop" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontFamily: mono, fontSize: 9 }}>
      {phase === 'confirm' ? (
        <span role="alertdialog" aria-modal="true" aria-label={t('chatMode.stop.confirmTitle')} onKeyDown={dialogKey} data-testid="chat-stop-confirm" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <span>{t('chatMode.stop.confirmBody')}</span>
          <button type="button" className="btn btn-danger btn-sm" onClick={confirmStop}>{t('chatMode.stop.confirm')}</button>
          <button ref={cancelBtn} type="button" className="btn btn-ghost btn-sm" onClick={() => setPhase(result ? 'done' : 'idle')}>{t('chatMode.stop.cancel')}</button>
        </span>
      ) : (offered || kind === 'warn') && (
        <button ref={stopBtn} type="button" className="btn btn-danger btn-sm" title={t('chatMode.stop.title')} disabled={phase === 'stopping' || stoppedNow} onClick={() => setPhase('confirm')}>
          <OctagonX size={11} aria-hidden="true" /> {phase === 'stopping' ? t('chatMode.stop.stopping') : stoppedNow ? t('chatMode.stop.refreshing') : kind === 'warn' ? t('chatMode.stop.retry') : t('chatMode.stop.button')}
        </button>
      )}
      {result && phase !== 'stopping' && (
        <span role={kind === 'ok' || kind === 'gone' ? 'status' : 'alert'} data-testid="chat-stop-result" data-ok={kind === 'ok' || kind === 'gone' ? 'yes' : kind === 'warn' ? 'warn' : 'no'} style={{ flexBasis: '100%' }}>
          {kind === 'gone' && <span>{t('chatMode.stop.alreadyStopped')}</span>}
          {kind === 'fail' && <strong>{t('chatMode.stop.failed')}</strong>}
          {kind === 'warn' && <strong style={{ color: 'var(--red, #ef4444)' }}>{t('chatMode.stop.partial')}</strong>}
          {kind !== 'gone' && [result?.output, result?.detail, result?.error].filter(Boolean).map((x, i) => (
            <pre key={i} style={{ margin: '2px 0', whiteSpace: 'pre-wrap' }}>
              {String(x).slice(0, 4000).split('\n').map((l, j) => (isWarnLine(l)
                ? <span key={j} data-testid="chat-stop-warn" role="alert" style={{ display: 'block', color: 'var(--red, #ef4444)', fontWeight: 700 }}>{l}</span>
                : <span key={j} style={{ display: 'block' }}>{l}</span>))}
            </pre>
          ))}
          <button type="button" className="btn btn-ghost btn-sm" onClick={dismiss}>{t('chatMode.stop.dismiss')}</button>
        </span>
      )}
    </span>
  )
}
