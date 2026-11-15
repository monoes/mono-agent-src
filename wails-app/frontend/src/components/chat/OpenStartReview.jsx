import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { openStartApi } from '../../lib/openStartApi.js'
import { CONTROL_ROOM_CHANGED } from '../../lib/useControlRoomProbe.js'

// The last step of the open-ended start: the preview the CLI rendered (the full hash, every lifted line, the plain
// limits) and one Approve button. There is nothing to type: the app derives the phrase itself and the CLI derives it
// again from the charter and compares. The barrier is the operating system's own dialog, which the app raises on Approve
// and which shows the lifted lines, the hash prefix and the budget; nothing starts without a Yes there.

const mono = 'var(--font-mono)'
const small = { fontFamily: mono, fontSize: 11, color: 'var(--text-muted)' }

// runWords is the started run in words, from the CLI's document: the run's id. The raw document is not shown.
function runWords(output, t) {
  try {
    const id = JSON.parse(output)?.run_id
    return id ? t('openStart.startedRun', { run: String(id) }) : ''
  } catch { return '' }
}

// firstTurnFailed is the first turn that did not queue, from the CLI's document: the run started, the boss has nothing to do yet.
function firstTurnFailed(output) {
  try { return JSON.parse(output)?.first_turn === 'failed' } catch { return false }
}

export function OpenStartReview({ charter, preview, getChat, onChat, onStarted, onUnknown }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [started, setStarted] = useState(null)
  const [unknown, setUnknown] = useState('') // the run may have started: no second start from here
  const [stale, setStale] = useState(false) // the preview's token is used or expired: Approve would repeat the refusal
  const ref = useRef(null)
  useEffect(() => { ref.current?.focus() }, [])
  useEffect(() => { setStale(false) }, [preview.start_token])
  const canStart = !!getChat && !!preview.start_token && !busy && !started && !unknown && !stale

  const start = async () => {
    setBusy(true); setError('')
    try {
      const r = await openStartApi.start(charter, preview.start_token, getChat())
      if (r?.chat) onChat?.(r.chat) // a chat the start made (after the yes) is the person's chat from now on, started or not
      if (r?.ok && r?.started) {
        setStarted(r); onStarted?.(r)
        window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)) // the nav asks again now, not at its next poll
      } else if (r?.state === 'unknown') { setUnknown(String(r.detail || t('openStart.unknown'))); onUnknown?.() }
      else {
        if (r?.code === 'token') setStale(true)
        setError(String(r?.detail || r?.error || t('openStart.startFailed')))
      }
    } catch (e) {
      // The call itself failed: whether the run started is not known, so this is not a plain error and not a retry.
      setUnknown(t('openStart.unknown') + ' ' + String(e?.message || e)); onUnknown?.()
    } finally {
      setBusy(false)
    }
  }

  return (
    <section data-testid="open-review" ref={ref} tabIndex={-1} aria-label={t('openStart.reviewTitle')} style={{ border: '1px solid var(--border)', borderRadius: 6, padding: 10, display: 'flex', flexDirection: 'column', gap: 6 }}>
      <span style={small}>{t('openStart.hash')}</span>
      <code style={{ fontFamily: mono, fontSize: 11, wordBreak: 'break-all' }}>{preview.hash}</code>
      <span style={small}>{t('openStart.liftedLines', { count: preview.lift_count })}</span>
      <ul style={{ margin: 0, paddingLeft: 16, ...small }}>{(preview.lines || []).map(l => <li key={l}>{l}</li>)}</ul>
      <pre style={{ ...small, whiteSpace: 'pre-wrap', margin: 0, maxHeight: 240, overflow: 'auto' }}>{preview.text}</pre>
      <p id="open-approve-intro" style={small}>{t('openStart.approveIntro')}</p>
      {!getChat && <p data-testid="open-no-boss" style={small}>{t('openStart.noBoss')}</p>}
      <button type="button" className="btn btn-sm" disabled={!canStart} aria-busy={busy} aria-describedby="open-approve-intro" onClick={start}>{t('openStart.approve')}</button>
      {busy && <p role="status" data-testid="open-waiting" style={small}>{t('openStart.waitingDialog')}</p>}
      {stale && <p data-testid="open-stale" style={small}>{t('openStart.previewStale')}</p>}
      {error && <p role="alert" style={{ ...small, color: 'var(--red)' }}>{error}</p>}
      {unknown && <p role="alert" data-testid="open-unknown" style={{ ...small, color: 'var(--red)' }}>{unknown}</p>}
      {started && <p data-testid="open-started" style={small}>{t('openStart.started')} {runWords(started.output, t)}</p>}
      {started && firstTurnFailed(started.output) && <p role="alert" data-testid="open-no-first-turn" style={{ ...small, color: 'var(--red)' }}>{t('openStart.noFirstTurn')}</p>}
    </section>
  )
}
