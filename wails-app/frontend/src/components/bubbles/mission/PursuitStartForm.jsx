import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LIMIT_MAX, expectedPhrase, limitOk, pursuitStartApi } from '../../../lib/pursuitStartApi.js'
import './mission.css'

// The start of a mission's pursuit, from the app. The CLI writes the preview (the goal whole, the limits, the dollars, what
// stops it); this shows it as it is, asks for the phrase typed by the person, and hands the phrase to the app, which asks again
// in a native dialog. The page never calls the start before a preview with a token has been read, never fills the phrase in
// for the person, and never starts a second time: an outcome that is not known is checked, not retried.

const LIMITS = [['parkedMax', 'parked_max'], ['pct', 'pct'], ['wakesMax', 'wakes_max']]
// the codes whose old preview is dead: the person must read again
const REREAD = new Set(['token', 'stale', 'consent_stale'])
const CODES = new Set(['operator_only', 'invalid_input', 'busy', 'token', 'phrase', 'blocked', 'declined', 'no_native_dialog', 'consent_stale', 'stale',
  'chat_mismatch', 'hidden_characters', 'criterion_too_long', 'already_started', 'unknown'])
const PREVIEW_CODES = new Set(['goal_too_long', 'unreadable', 'busy', 'rate_limited', 'operator_only'])

const Alert = ({ children, ...rest }) => (children ? <p className="ms-error" role="alert" {...rest}>{children}</p> : null)

function Review({ sessionId, preview, onReread }) {
  const { t } = useTranslation()
  const [phrase, setPhrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [started, setStarted] = useState(false)
  const [unknown, setUnknown] = useState(false)
  const [note, setNote] = useState(null) // {code, text}: why the last press did not start it
  const ref = useRef(null)
  useEffect(() => { ref.current?.focus() }, [])
  const want = expectedPhrase(preview)
  const blockers = [...(preview.blockers || [])]
  if (preview.rubric_present === false) blockers.push({ code: 'no-rubric', reason: '' })
  if (preview.host?.loop_on === false) blockers.push({ code: 'host-off', reason: '' })
  const typed = !!want && phrase.trim() === want
  const done = started || unknown
  const canStart = typed && !!preview.start_token && blockers.length === 0 && !busy && !done
  const lim = { parkedMax: preview.limits.parked_max, pct: preview.limits.pct, wakesMax: preview.limits.wakes_max }

  const press = async () => {
    if (!canStart) return
    setBusy(true); setNote(null)
    let r
    try {
      r = await pursuitStartApi.start(sessionId, lim, phrase, preview.start_token)
    } catch (e) {
      // The call itself failed: whether it started is not known, so this is not a plain error and not a retry.
      setUnknown(true); setNote({ code: 'unknown', text: t('pursuitStart.unknown') + ' ' + String(e?.message || e) }); setBusy(false)
      return
    }
    setBusy(false)
    if (r?.ok && r?.started) { setStarted(true); return }
    const code = String(r?.code || '')
    if (r?.state === 'unknown' || code === 'unknown') { setUnknown(true); setNote({ code: 'unknown', text: t('pursuitStart.unknown') }); return }
    const text = CODES.has(code) ? t(`pursuitStart.code.${code}`) : t('pursuitStart.notStarted')
    setNote({ code, text, detail: String(r?.detail || r?.error || '') })
    if (code === 'already_started') setUnknown(true) // it is running: nothing more to press here
  }

  return (
    <div data-testid="pursuit-review" ref={ref} tabIndex={-1} className="ms-pursuit" aria-label={t('pursuitStart.reviewLabel')}>
      <strong>{t('pursuitStart.goalHeading')}</strong>
      <span>{t('pursuitStart.hashLabel')} <code data-testid="pursuit-hash">{preview.goal.hash}</code></span>
      <ul data-testid="pursuit-criteria">
        {preview.goal.criteria.map(c => (
          <li key={c.id}><code>{c.id} [{c.kind}]</code> {c.spec}{c.expected ? <> ({t('pursuitStart.expects')} <code>{c.expected}</code>)</> : null}</li>
        ))}
      </ul>
      <strong>{t('pursuitStart.costHeading')}</strong>
      <ul data-testid="pursuit-cost">{(preview.cost?.rows || []).map(r => <li key={r.key}>{r.text}</li>)}</ul>
      <strong>{t('pursuitStart.awayHeading')}</strong>
      <ul data-testid="pursuit-away">
        <li>{t('pursuitStart.awayPct', { count: preview.limits.pct })}</li>
        <li>{t('pursuitStart.awayParked', { count: preview.limits.parked_max })}</li>
        <li>{t('pursuitStart.awayWakes', { count: preview.limits.wakes_max })}</li>
      </ul>
      {preview.host?.judge_pin ? <span data-testid="pursuit-judge">{t('pursuitStart.judge', { pin: preview.host.judge_pin })}</span> : null}
      {preview.checker && (preview.checker.set_up || preview.checker.unconfined || preview.checker.command_criteria > 0) ? (
        <div data-testid="pursuit-checker">
          <strong>{t('pursuitStart.checkerHeading')}</strong>
          <ul>
            {preview.checker.set_up ? <li>{t('pursuitStart.checkerSetUp')}</li> : null}
            {preview.checker.command_criteria > 0 ? <li>{t('pursuitStart.checkerCommands', { count: preview.checker.command_criteria })}</li> : null}
            {preview.checker.unconfined ? <li data-testid="pursuit-unconfined">{t('pursuitStart.checkerUnconfined', { state: String(preview.checker.unconfined) })}</li> : null}
          </ul>
        </div>
      ) : null}
      {blockers.length > 0 && (
        <div data-testid="pursuit-blockers">
          <strong>{t('pursuitStart.blockerHeading')}</strong>
          <ul>{blockers.map(b => <li key={b.code}><code>{b.code}</code> {b.reason}</li>)}</ul>
        </div>
      )}
      <p className="ms-muted">{t('pursuitStart.phraseIntro')}</p>
      <code data-testid="pursuit-phrase-template">{want}</code>
      <label>
        {t('pursuitStart.phraseLabel')}
        <input aria-label={t('pursuitStart.phraseLabel')} value={phrase} onChange={e => setPhrase(e.target.value)} autoComplete="off" spellCheck={false} disabled={done} />
      </label>
      <div className="ms-actions">
        <button type="button" className="btn btn-primary btn-sm" data-testid="pursuit-start" disabled={!canStart} onClick={press}>{t('pursuitStart.start')}</button>
        {!done && <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={onReread}>{t('pursuitStart.reload')}</button>}
      </div>
      {note && (
        <div role="alert" data-testid={`pursuit-note-${note.code || 'none'}`} className="ms-error">
          {note.text}{note.detail && note.code !== 'unknown' ? <> <code>{note.detail}</code></> : null}
          {REREAD.has(note.code) && <> <button type="button" className="btn btn-ghost btn-sm" onClick={onReread}>{t('pursuitStart.reload')}</button></>}
        </div>
      )}
      {started && <p className="ms-muted" data-testid="pursuit-started">{t('pursuitStart.started')}</p>}
    </div>
  )
}

export default function PursuitStartForm({ sessionId }) {
  const { t } = useTranslation()
  const [lim, setLim] = useState({ parkedMax: '', pct: '', wakesMax: '' })
  const [preview, setPreview] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [round, setRound] = useState(0)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const bad = LIMITS.filter(([k]) => !limitOk(k, lim[k])).map(([k]) => k)

  const read = async () => {
    if (bad.length) return
    setBusy(true); setError(''); setPreview(null)
    try {
      const r = await pursuitStartApi.preview(sessionId, { parkedMax: Number(lim.parkedMax) || 0, pct: Number(lim.pct) || 0, wakesMax: Number(lim.wakesMax) || 0 })
      if (!alive.current) return
      if (r?.error || !r?.start_token || !r?.goal?.hash) {
        const code = String(r?.code || '')
        setError(PREVIEW_CODES.has(code) ? t(`pursuitStart.previewCode.${code}`) + ' ' + String(r?.error || '') : String(r?.error || r?.detail || t('pursuitStart.previewFailed')))
      } else { setPreview(r); setRound(n => n + 1) }
    } catch (e) {
      if (alive.current) setError(String(e?.message || e))
    } finally {
      if (alive.current) setBusy(false)
    }
  }
  const change = (k, v) => { setLim(l => ({ ...l, [k]: v })); setPreview(null) } // other limits, other preview

  return (
    <div className="ms-pursuit" data-testid="pursuit-form">
      <p className="ms-muted">{t('pursuitStart.intro')}</p>
      <div className="ms-pursuit-limits">
        {LIMITS.map(([k]) => (
          <label key={k}>
            {t(`pursuitStart.limit.${k}`)}
            <input type="number" inputMode="numeric" min={1} max={LIMIT_MAX[k]} step={1} aria-label={t(`pursuitStart.limit.${k}`)} value={lim[k]} placeholder={t('pursuitStart.default')}
              aria-invalid={bad.includes(k) ? 'true' : undefined} onChange={e => change(k, e.target.value)} />
          </label>
        ))}
      </div>
      {bad.length > 0 && <Alert data-testid="pursuit-limit-error">{t('pursuitStart.limitBounds', { pct: LIMIT_MAX.pct })}</Alert>}
      <div className="ms-actions">
        <button type="button" className="btn btn-secondary btn-sm" data-testid="pursuit-read" disabled={busy || bad.length > 0} onClick={read}>{t('pursuitStart.read')}</button>
      </div>
      <Alert data-testid="pursuit-preview-error">{error}</Alert>
      {preview && <Review key={`${round}:${preview.start_token}`} sessionId={sessionId} preview={preview} onReread={read} />}
    </div>
  )
}
