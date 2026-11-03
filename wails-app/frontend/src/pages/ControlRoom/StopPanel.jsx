import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { OctagonX } from 'lucide-react'
import { clip } from './controlRoomModel.js'

// The only action. Two steps (click, then confirm); the call takes no argument and the CLI re-checks the operator
// gate. The CLI's outcome is shown as it came: "NOT stopped", survivors and "NOT RECORDED" are never softened.
export default function StopPanel({ api, disabled, onDone }) {
  const { t } = useTranslation()
  const [phase, setPhase] = useState('idle') // idle | confirm | stopping | done
  const [result, setResult] = useState(null)

  const confirmStop = async () => {
    setPhase('stopping')
    const r = await api.stop()
    setResult(r)
    setPhase('done')
    if (onDone) onDone()
  }
  const failed = phase === 'done' && !(result?.ok && result?.stopped)
  return (
    <section className="cr-card cr-stop" aria-label={t('controlRoom.stop.title')} data-testid="cr-stop">
      <h3>{t('controlRoom.stop.title')}</h3>
      <p className="cr-meta">{t('controlRoom.stop.cannot')}</p>
      {phase === 'confirm' ? (
        <div role="alertdialog" aria-label={t('controlRoom.stop.confirmTitle')} className="cr-confirm" data-testid="cr-stop-confirm">
          <p>{t('controlRoom.stop.confirmBody')}</p>
          <button type="button" className="btn btn-danger btn-sm" onClick={confirmStop}>{t('controlRoom.stop.confirm')}</button>
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setPhase('idle')}>{t('controlRoom.stop.cancel')}</button>
        </div>
      ) : (
        <button type="button" className="btn btn-danger" disabled={disabled || phase === 'stopping'} onClick={() => setPhase('confirm')}>
          <OctagonX size={14} aria-hidden="true" /> {phase === 'stopping' ? t('controlRoom.stop.stopping') : t('controlRoom.stop.button')}
        </button>
      )}
      {phase === 'done' ? (
        <div role={failed ? 'alert' : 'status'} className={failed ? 'cr-warn' : 'cr-meta'} data-testid="cr-stop-result" data-ok={failed ? 'no' : 'yes'}>
          {failed ? <strong>{t('controlRoom.stop.failed')}</strong> : null}
          {result?.output ? <pre className="cr-pre">{clip(result.output, 4000)}</pre> : null}
          {result?.detail ? <pre className="cr-pre">{clip(result.detail, 4000)}</pre> : null}
          {result?.error ? <pre className="cr-pre">{clip(result.error, 4000)}</pre> : null}
        </div>
      ) : null}
    </section>
  )
}
