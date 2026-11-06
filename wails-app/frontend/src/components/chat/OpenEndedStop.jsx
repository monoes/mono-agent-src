import { useState } from 'react'
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

export function OpenEndedStop({ chatMode, runExists, conversationId, onStopped, api = controlRoomApi }) {
  const { t } = useTranslation()
  const [phase, setPhase] = useState('idle') // idle | confirm | stopping | done
  const [result, setResult] = useState(null)
  if (!showStop(chatMode?.state?.mode, runExists)) return null

  const confirmStop = async () => {
    setPhase('stopping')
    const r = await api.stop()
    setResult(r)
    setPhase('done')
    if (r?.ok && r?.stopped && onStopped) {
      const line = stopLine(r.output)
      onStopped(transcriptMessage(r.output, conversationId) || t('chatMode.stop.message', { time: new Date().toLocaleString(), line }))
    }
  }
  const failed = phase === 'done' && !(result?.ok && result?.stopped)
  return (
    <span data-testid="chat-stop" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontFamily: mono, fontSize: 9 }}>
      {phase === 'confirm' ? (
        <span role="alertdialog" aria-label={t('chatMode.stop.confirmTitle')} data-testid="chat-stop-confirm" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <span>{t('chatMode.stop.confirmBody')}</span>
          <button type="button" className="btn btn-danger btn-sm" onClick={confirmStop}>{t('chatMode.stop.confirm')}</button>
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setPhase('idle')}>{t('chatMode.stop.cancel')}</button>
        </span>
      ) : (
        <button type="button" className="btn btn-danger btn-sm" title={t('chatMode.stop.title')} disabled={phase === 'stopping'} onClick={() => setPhase('confirm')}>
          <OctagonX size={11} aria-hidden="true" /> {phase === 'stopping' ? t('chatMode.stop.stopping') : t('chatMode.stop.button')}
        </button>
      )}
      {phase === 'done' && (
        <span role={failed ? 'alert' : 'status'} data-testid="chat-stop-result" data-ok={failed ? 'no' : 'yes'} style={{ flexBasis: '100%' }}>
          {failed && <strong>{t('chatMode.stop.failed')}</strong>}
          {[result?.output, result?.detail, result?.error].filter(Boolean).map((x, i) => <pre key={i} style={{ margin: '2px 0', whiteSpace: 'pre-wrap' }}>{String(x).slice(0, 4000)}</pre>)}
        </span>
      )}
    </span>
  )
}
