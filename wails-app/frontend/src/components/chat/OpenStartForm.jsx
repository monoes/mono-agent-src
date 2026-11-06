import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { openStartApi } from '../../lib/openStartApi.js'
import { MAX_TEXT, ASK_ACCESS, LIMIT_KEYS, LIMIT_PRESETS, EVERYTHING_LINES, LINES, buildCharter, emptyForm } from '../../lib/openCharter.js'
import { OpenStartReview } from './OpenStartReview.jsx'

// The entry screen and the start form of the open-ended chat mode. The CLI decides what can start (the probe), what the
// charter means (render) and whether the typed phrase is right (start); this shows the plain statements the person
// must read, builds the charter from the form, and hands the phrase they write to the CLI. ensureChat is an async
// function that answers with the id of the chat (creating it, in the open mode, if it does not exist yet): the CLI makes the
// boss behind that chat when the run starts. Without it the run cannot be started from here.

const mono = 'var(--font-mono)'
const box = { border: '1px solid var(--border)', borderRadius: 6, padding: 10, display: 'flex', flexDirection: 'column', gap: 6 }
const small = { fontFamily: mono, fontSize: 11, color: 'var(--text-muted)' }
const field = { fontFamily: mono, fontSize: 11 }

// errorId is the id of the message that explains why the field is refused; it links it to the field.
function Text({ id, label, value, onChange, area, errorId, ...rest }) {
  const Tag = area ? 'textarea' : 'input'
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 2, ...small }}>
      {label}
      <Tag id={id} aria-label={label} value={value} onChange={e => onChange(e.target.value)} style={field}
        aria-describedby={errorId} aria-invalid={errorId ? 'true' : undefined} {...rest} />
    </label>
  )
}

const Err = ({ id, children }) => (children ? <span id={id} role="alert" style={{ ...small, color: 'var(--red)' }}>{children}</span> : null)

const PinFields = ({ t, who, pin, onChange, errorId }) => (
  <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
    {['provider', 'model', 'version'].map(k => (
      <Text key={k} label={t(`openStart.${who}${k[0].toUpperCase()}${k.slice(1)}`)} value={pin[k]} errorId={errorId} onChange={v => onChange({ ...pin, [k]: v })} />
    ))}
  </div>
)

export function OpenStartForm({ ensureChat = null, onStarted }) {
  const { t } = useTranslation()
  const [entry, setEntry] = useState(null)
  const [form, setForm] = useState(emptyForm)
  const [errors, setErrors] = useState({})
  const [review, setReview] = useState(null) // { charter, preview } once the CLI has rendered it
  const [reviewError, setReviewError] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false) // started: nothing can be submitted again
  const [mayExist, setMayExist] = useState(false) // a start ended unknown: a run may exist

  useEffect(() => {
    let live = true
    openStartApi.entry().then(r => { if (live) setEntry(r) }).catch(e => { if (live) setEntry({ error: String(e?.message || e) }) })
    return () => { live = false }
  }, [])

  const set = patch => { setForm(f => ({ ...f, ...patch })); setReview(null) }
  const criteria = form.criteria
  const setCriterion = (i, patch) => set({ criteria: criteria.map((c, j) => (j === i ? { ...c, ...patch } : c)) })
  const toggleLine = l => set({ lines: form.lines.includes(l) ? form.lines.filter(x => x !== l) : [...form.lines, l] })
  const preset = name => set({ limitsPreset: name, limits: { ...LIMIT_PRESETS[name] } })
  const perAsk = (patch) => set({ perAsk: { ...form.perAsk, ...patch } })
  const err = useMemo(() => Object.fromEntries(Object.entries(errors).map(([k]) => [k, t(`openStart.err.${k}`)])), [errors, t])

  const doReview = async () => {
    const { charter, errors: found } = buildCharter(form)
    setErrors(found); setReviewError(''); setReview(null)
    if (!charter) return
    setBusy(true)
    try {
      const preview = await openStartApi.render(charter)
      if (preview?.error || !preview?.hash) setReviewError(String(preview?.error || preview?.detail || t('openStart.previewFailed')))
      else setReview({ charter, preview })
    } catch (e) {
      setReviewError(String(e?.message || e))
    } finally {
      setBusy(false)
    }
  }

  if (!entry) return <div role="status" style={small}>{t('openStart.loading')}</div>
  return (
    <div data-testid="open-start" style={{ display: 'flex', flexDirection: 'column', gap: 10, padding: 12, overflowY: 'auto' }}>
      <section style={box} aria-label={t('openStart.entryTitle')}>
        <strong style={{ fontFamily: mono, fontSize: 12 }}>{t('openStart.entryTitle')}</strong>
        <p style={small}>{t('openStart.noApproval')}</p>
        <p style={small}>{t('openStart.steer')}</p>
        <p style={small}>{t('openStart.stopHeader')}</p>
        {entry.not_defended && (
          <>
            <span data-testid="open-platform" style={small}>{t('openStart.platform', { platform: entry.platform || '' })}</span>
            <pre data-testid="open-not-defended" style={{ ...small, whiteSpace: 'pre-wrap', margin: 0 }}>{entry.not_defended}</pre>
          </>
        )}
      </section>
      {!entry.mode_available ? (
        <p role="alert" data-testid="open-unavailable" style={{ ...small, color: 'var(--red)' }}>
          {t('openStart.unavailable', { reason: entry.reason || entry.error || entry.detail || t('openStart.unknownReason') })}
        </p>
      ) : (
        <fieldset disabled={done} style={{ border: 0, padding: 0, margin: 0, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 10 }}>
          <section style={box}>
            <Text label={t('openStart.goal')} value={form.goal} onChange={v => set({ goal: v })} area rows={3} errorId={err.goal ? 'open-err-goal' : undefined} />
            <span data-testid="open-goal-count" style={small}>{[...form.goal].length}/{MAX_TEXT}</span>
            <Err id="open-err-goal">{err.goal}</Err>
            <Err id="open-err-text">{err.text}</Err>
            {criteria.map((c, i) => (
              <div key={i} style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
                {c.kind === 'command' ? (
                  <>
                    <Text label={t('openStart.criterionCommand', { n: i + 1 })} value={c.command || ''} onChange={v => setCriterion(i, { command: v })} />
                    <Text label={t('openStart.criterionExpected', { n: i + 1 })} value={c.expected || ''} onChange={v => setCriterion(i, { expected: v })} />
                  </>
                ) : (
                  <Text label={t('openStart.criterionRubric', { n: i + 1 })} value={c.text || ''} onChange={v => setCriterion(i, { text: v })} />
                )}
                <button type="button" className="btn btn-ghost btn-sm" aria-label={t('openStart.removeN', { n: i + 1 })} onClick={() => set({ criteria: criteria.filter((_, j) => j !== i) })}>{t('openStart.remove')}</button>
              </div>
            ))}
            <div style={{ display: 'flex', gap: 6 }}>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ criteria: [...criteria, { kind: 'command', command: '', expected: '' }] })}>{t('openStart.addCommand')}</button>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ criteria: [...criteria, { kind: 'rubric', text: '' }] })}>{t('openStart.addRubric')}</button>
            </div>
            <Err id="open-err-criteria">{err.criteria}</Err>
          </section>

          <section style={box}>
            <span style={small}>{t('openStart.limits')}</span>
            <div style={{ display: 'flex', gap: 6 }}>
              {Object.keys(LIMIT_PRESETS).map(name => (
                <button key={name} type="button" className="btn btn-ghost btn-sm" aria-pressed={form.limitsPreset === name} onClick={() => preset(name)}>{t(`openStart.preset.${name}`)}</button>
              ))}
            </div>
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {LIMIT_KEYS.map(k => (
                <Text key={k} label={t(`openStart.limit.${k}`)} errorId={err.limits ? 'open-err-limits' : undefined} value={String(form.limits[k])} onChange={v => set({ limitsPreset: 'custom', limits: { ...form.limits, [k]: v } })} />
              ))}
            </div>
            <Err id="open-err-limits">{err.limits}</Err>
          </section>

          <fieldset style={box} aria-describedby={err.budget ? 'open-err-budget' : undefined}>
            <legend style={small}>{t('openStart.budget')}</legend>
            <label style={small}><input type="radio" name="open-budget" checked={form.budget.choice === 'unlimited'} onChange={() => set({ budget: { choice: 'unlimited', usd: '' } })} /> {t('openStart.budgetUnlimited')}</label>
            <label style={small}><input type="radio" name="open-budget" checked={form.budget.choice === 'fixed'} onChange={() => set({ budget: { ...form.budget, choice: 'fixed' } })} /> {t('openStart.budgetFixed')}</label>
            {form.budget.choice === 'fixed' && <Text label={t('openStart.budgetUsd')} value={form.budget.usd} onChange={v => set({ budget: { choice: 'fixed', usd: v } })} inputMode="decimal" />}
            <Err id="open-err-budget">{err.budget}</Err>
          </fieldset>

          <fieldset style={box}>
            <legend style={small}>{t('openStart.linesTitle')}</legend>
            <div style={{ display: 'flex', gap: 6 }}>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ lines: [...EVERYTHING_LINES] })}>{t('openStart.linesEverything')}</button>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ lines: [] })}>{t('openStart.linesNone')}</button>
            </div>
            {LINES.map(l => (
              <label key={l} data-testid={`open-line-${l}`} style={small}>
                <input type="checkbox" aria-label={t(`openStart.line.${l}`)} checked={form.lines.includes(l)} onChange={() => toggleLine(l)} /> {t(`openStart.line.${l}`)}
                {l === 'platform_caps' && <em> {t('openStart.byNameOnly')}</em>}
              </label>
            ))}
          </fieldset>

          <section style={box}>
            <span style={small}>{t('openStart.pins')}</span>
            <PinFields t={t} who="boss" pin={form.pins.boss} errorId={err.boss ? 'open-err-boss' : undefined} onChange={p => set({ pins: { ...form.pins, boss: p } })} />
            <Err id="open-err-boss">{err.boss}</Err>
            <PinFields t={t} who="verifier" pin={form.pins.verifier} errorId={err.verifier ? 'open-err-verifier' : undefined} onChange={p => set({ pins: { ...form.pins, verifier: p } })} />
            <Err id="open-err-verifier">{err.verifier}</Err>
            <Text label={t('openStart.judging')} value={form.pins.judging} errorId={err.judging ? 'open-err-judging' : undefined} onChange={v => set({ pins: { ...form.pins, judging: v } })} />
            <Err id="open-err-judging">{err.judging}</Err>
          </section>

          <section style={box}>
            <label style={small}><input type="checkbox" checked={form.perAsk.enabled} onChange={e => perAsk({ enabled: e.target.checked })} /> {t('openStart.perAskOn')}</label>
            {!form.perAsk.enabled && <span style={small}>{t('openStart.perAskDeny')}</span>}
            {form.perAsk.enabled && (
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }} aria-describedby={err.perAsk ? 'open-err-perask' : undefined}>
                <Text label={t('openStart.askSpend')} value={form.perAsk.spendUsd} onChange={v => perAsk({ spendUsd: v })} />
                <Text label={t('openStart.askTasks')} value={form.perAsk.tasks} onChange={v => perAsk({ tasks: v })} />
                <Text label={t('openStart.askDepth')} value={form.perAsk.depth} onChange={v => perAsk({ depth: v })} />
                <Text label={t('openStart.askRunSpend')} value={form.perAsk.runSpendUsd} onChange={v => perAsk({ runSpendUsd: v })} />
                <Text label={t('openStart.askRunTasks')} value={form.perAsk.runTasks} onChange={v => perAsk({ runTasks: v })} />
                <label style={{ display: 'flex', flexDirection: 'column', ...small }}>{t('openStart.askAccess')}
                  <select aria-label={t('openStart.askAccess')} value={form.perAsk.access} onChange={e => perAsk({ access: e.target.value })} style={field}>
                    {ASK_ACCESS.map(a => <option key={a} value={a}>{a}</option>)}
                  </select>
                </label>
              </div>
            )}
            <Err id="open-err-perask">{err.perAsk}</Err>
          </section>

          {mayExist && <p role="alert" data-testid="open-may-exist" style={{ ...small, color: 'var(--red)' }}>{t('openStart.mayExist')}</p>}
          <button type="button" className="btn btn-sm" disabled={busy || done} onClick={doReview}>{t('openStart.review')}</button>
          {reviewError && <p role="alert" style={{ ...small, color: 'var(--red)' }}>{reviewError}</p>}
          {review && <OpenStartReview key={review.preview.start_token} charter={review.charter} preview={review.preview} ensureChat={ensureChat} onStarted={r => { setDone(true); onStarted?.(r) }} onUnknown={() => setMayExist(true)} />}
        </fieldset>
      )}
    </div>
  )
}
