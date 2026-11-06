import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { openStartApi } from '../../lib/openStartApi.js'
import { CONTROL_ROOM_CHANGED } from '../../lib/useControlRoomProbe.js'

// The last step of the open-ended start: the preview the CLI rendered (the full hash, every lifted line, the plain
// limits) and the phrase the person types. The phrase is never shown or pre-filled: the template says what it is made
// of (the number of lifted lines and the first 8 characters of the hash, both on this screen), the person writes it,
// and the CLI derives it again from the charter and compares. Nothing starts without it.

const mono = 'var(--font-mono)'
const small = { fontFamily: mono, fontSize: 11, color: 'var(--text-muted)' }

export function OpenStartReview({ charter, preview, bossSession, onStarted }) {
  const { t } = useTranslation()
  const [phrase, setPhrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [started, setStarted] = useState(null)
  const [unknown, setUnknown] = useState('') // the run may have started: no second start from here
  const ref = useRef(null)
  useEffect(() => { ref.current?.focus() }, [])
  const canStart = !!phrase.trim() && !!bossSession && !!preview.start_token && !busy && !started && !unknown

  const start = async () => {
    setBusy(true); setError('')
    try {
      const r = await openStartApi.start(charter, phrase, preview.start_token, bossSession)
      if (r?.ok && r?.started) {
        setStarted(r); onStarted?.(r)
        window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)) // the nav asks again now, not at its next poll
      } else if (r?.state === 'unknown') setUnknown(String(r.detail || t('openStart.unknown')))
      else setError(String(r?.detail || r?.error || t('openStart.startFailed')))
    } catch (e) {
      // The call itself failed: whether the run started is not known, so this is not a plain error and not a retry.
      setUnknown(t('openStart.unknown') + ' ' + String(e?.message || e))
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
      <p style={small}>{t('openStart.phraseIntro')}</p>
      <code data-testid="open-phrase-template" style={{ fontFamily: mono, fontSize: 11 }}>{preview.phrase_template}</code>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 2, ...small }}>
        {t('openStart.phraseLabel')}
        <input aria-label={t('openStart.phraseLabel')} value={phrase} onChange={e => setPhrase(e.target.value)} autoComplete="off" spellCheck={false} style={{ fontFamily: mono, fontSize: 11 }} />
      </label>
      {!bossSession && <p data-testid="open-no-boss" style={small}>{t('openStart.noBoss')}</p>}
      <button type="button" className="btn btn-sm" disabled={!canStart} onClick={start}>{t('openStart.start')}</button>
      {error && <p role="alert" style={{ ...small, color: 'var(--red)' }}>{error}</p>}
      {unknown && <p role="alert" data-testid="open-unknown" style={{ ...small, color: 'var(--red)' }}>{unknown}</p>}
      {started && <p data-testid="open-started" style={small}>{t('openStart.started')} {started.output}</p>}
    </section>
  )
}
