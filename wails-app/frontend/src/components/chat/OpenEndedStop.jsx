import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { OctagonX } from 'lucide-react'
import { controlRoomApi } from '../../services/controlRoom.js'

const mono = 'var(--font-mono)'

// The Stop of an open-ended run, in the chat header. It is the control room's Stop (the same binding, the same two steps,
// and it works signed out): the CLI does the stopping, ends the run's sessions and writes a message into the transcript,
// and this shows what the command said without softening it. Offered when the chat is open-ended and the profile's run is
// not known to be gone, or whenever the probe says a run exists; a normal chat with no run never shows it.
export function showStop(mode, runExists) {
  return runExists === true || (mode === 'open' && runExists !== false)
}

// stopLine is the verdict line of the command's output ("stopped", or what the CLI says instead).
export function stopLine(output) {
  const lines = String(output || '').split('\n').map(l => l.trim()).filter(Boolean)
  return lines.find(l => /^(stopped|NOT stopped|Stop )/.test(l)) || lines[0] || ''
}

// transcriptMessage is the message the CLI wrote into this chat's transcript, from its own "transcript <chat>: <message>"
// line; '' when it wrote none (the chat has no session of the run).
export function transcriptMessage(output, conversationId) {
  const prefix = `transcript ${conversationId}: `
  const line = String(output || '').split('\n').find(l => l.startsWith(prefix))
  return line ? line.slice(prefix.length) : ''
}

// isRepair: a later stop that found no open run and finished what the previous one left (exit 0, no "stopped" first line).
export function isRepair(output) {
  return /^no open run; (ended sessions|wrote the chat message)/.test(String(output || '').trim())
}

// A line the CLI printed about something it could not do: shown as a warning, never as a success.
export function isWarnLine(line) {
  return /\bNOT (ended|written|stopped|RECORDED)\b|cannot be read|^Stop /.test(line)
}

// outcome classifies a Stop result: 'ok' (stopped, or a later stop finished the previous one's cleanup), 'warn' (the run IS
// stopped but the CLI could not finish its sessions or message: exit 8, first line still "stopped"), 'fail' (anything else).
export function outcome(result) {
  const out = String(result?.output || '').trim()
  if (result?.ok && (result?.stopped || isRepair(out))) return 'ok'
  if (!result?.ok && /^(stopped|no open run; (ended sessions|wrote the chat message|the previous stop))/.test(out)) return 'warn'
  return 'fail'
}

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

  const offered = showStop(chatMode?.state?.mode, runExists)
  // The result stays until dismissed, even when the probe has caught up and says there is no run any more.
  if (!offered && !result && phase === 'idle') return null

  const confirmStop = async () => {
    setPhase('stopping')
    const r = await api.stop()
    setResult(r)
    setPhase('done')
    const kind = outcome(r)
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
      ) : offered && (
        <button ref={stopBtn} type="button" className="btn btn-danger btn-sm" title={t('chatMode.stop.title')} disabled={phase === 'stopping' || stoppedNow} onClick={() => setPhase('confirm')}>
          <OctagonX size={11} aria-hidden="true" /> {phase === 'stopping' ? t('chatMode.stop.stopping') : stoppedNow ? t('chatMode.stop.refreshing') : kind === 'warn' ? t('chatMode.stop.retry') : t('chatMode.stop.button')}
        </button>
      )}
      {result && phase !== 'stopping' && (
        <span role={kind === 'ok' ? 'status' : 'alert'} data-testid="chat-stop-result" data-ok={kind === 'ok' ? 'yes' : kind === 'warn' ? 'warn' : 'no'} style={{ flexBasis: '100%' }}>
          {kind === 'fail' && <strong>{t('chatMode.stop.failed')}</strong>}
          {kind === 'warn' && <strong style={{ color: 'var(--red, #ef4444)' }}>{t('chatMode.stop.partial')}</strong>}
          {[result?.output, result?.detail, result?.error].filter(Boolean).map((x, i) => (
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
