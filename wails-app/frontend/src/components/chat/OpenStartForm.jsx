import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { openStartApi } from '../../lib/openStartApi.js'
import { MAX_TEXT, ASK_ACCESS, LIMIT_KEYS, LIMIT_PRESETS, EVERYTHING_LINES, LINES, JUDGING_POLICIES, PER_ASK_CHOICES, buildCharter, emptyForm, modelChoices, catalogChoices, defaultPins } from '../../lib/openCharter.js'
import { OpenStartReview } from './OpenStartReview.jsx'
import { unboundText } from './useRunUnbound.js'

// The entry screen and the start form of the open-ended chat mode. The CLI decides what can start (the probe), what the
// charter means (render) and whether the typed phrase is right (start); this shows the plain statements the person
// must read, builds the charter from the form, and hands the phrase they write to the CLI. getChat is a function that answers
// with the chat the run is for: {id} of a chat that exists, or {spec} of one the start makes after the person said yes (onChat
// then gets it). The CLI makes the boss behind that chat when the run starts. Without it the run cannot be started from here.

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

// A list of values. A value that is not in the list (a form from before, a model no longer in the roster) is still shown, so it is never hidden.
function Pick({ label, value, options, onChange, errorId, t }) {
  const shown = value && !options.includes(value) ? [value, ...options] : options
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 2, ...small }}>
      {label}
      <select aria-label={label} value={value} onChange={e => onChange(e.target.value)} style={field}
        aria-describedby={errorId} aria-invalid={errorId ? 'true' : undefined}>
        {!value && <option value="">{t('openStart.choose')}</option>}
        {shown.map(o => <option key={o} value={o}>{o}</option>)}
      </select>
    </label>
  )
}

const uniq = list => [...new Set(list)]

// With a roster the pin is three linked lists: the provider, then the models of that provider, then its versions. Without
// one (no model validated yet, or it could not be read) the three are typed as before.
function PinFields({ t, who, pin, onChange, errorId, choices }) {
  const names = { provider: 'Provider', model: 'Model', version: 'Version' }
  const label = k => t(`openStart.${who}${names[k]}`)
  if (!choices.length) {
    return (
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {Object.keys(names).map(k => <Text key={k} label={label(k)} value={pin[k]} errorId={errorId} onChange={v => onChange({ ...pin, [k]: v })} />)}
      </div>
    )
  }
  const ofProvider = choices.filter(c => c.provider === pin.provider)
  const ofModel = ofProvider.filter(c => c.model === pin.model)
  const choose = c => onChange({ provider: c.provider, model: c.model, version: c.version })
  return (
    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
      <Pick t={t} label={label('provider')} value={pin.provider} errorId={errorId} options={uniq(choices.map(c => c.provider))}
        onChange={v => choose(choices.find(c => c.provider === v))} />
      <Pick t={t} label={label('model')} value={pin.model} errorId={errorId} options={uniq(ofProvider.map(c => c.model))}
        onChange={v => choose(ofProvider.find(c => c.model === v))} />
      <Pick t={t} label={label('version')} value={pin.version} errorId={errorId} options={uniq(ofModel.map(c => c.version))}
        onChange={v => onChange({ ...pin, version: v })} />
    </div>
  )
}

export function OpenStartForm({ getChat = null, onChat, onStarted }) {
  const { t } = useTranslation()
  const [entry, setEntry] = useState(null)
  const [form, setForm] = useState(emptyForm)
  const [errors, setErrors] = useState({})
  const [review, setReview] = useState(null) // { charter, preview } once the CLI has rendered it
  const [reviewError, setReviewError] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false) // started: nothing can be submitted again
  const [mayExist, setMayExist] = useState(false) // a start ended unknown: a run may exist
  const [choices, setChoices] = useState([]) // the models the pins can name, from the validated roster; empty: type the pins

  useEffect(() => {
    let live = true
    openStartApi.entry().then(r => { if (live) setEntry(r) }).catch(e => { if (live) setEntry({ error: String(e?.message || e) }) })
    // The models only fill the lists and the first pins: any failure leaves the typed fields.
    const apply = found => {
      if (!live || !found.length) return false
      setChoices(found)
      // Pins the person has not touched start as a boss and a different verifier; the form never overwrites what they chose.
      setForm(f => (f.pins.boss.provider || f.pins.verifier.provider ? f : { ...f, pins: { ...f.pins, ...defaultPins(found) } }))
      return true
    }
    // The validated roster first; a machine with none falls back to the installed runtimes' own model lists.
    Promise.resolve().then(() => openStartApi.roster?.()).then(r => modelChoices(r)).catch(() => [])
      .then(found => apply(found) || Promise.resolve().then(() => openStartApi.catalog?.()).then(c => apply(catalogChoices(c))))
      .catch(() => {})
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
      const preview = await openStartApi.render(charter, getChat?.())
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
        <p data-testid="open-howto" style={small}>{t('openStart.howTo')}</p>
        {entry.not_defended && (
          <>
            <span data-testid="open-platform" style={small}>{t('openStart.platform', { platform: entry.platform || '' })}</span>
            <pre data-testid="open-not-defended" style={{ ...small, whiteSpace: 'pre-wrap', margin: 0 }}>{entry.not_defended}</pre>
          </>
        )}
      </section>
      {entry.mode_available && entry.warning && (
        <p role="alert" data-testid="open-unbound-warning" style={{ ...small, color: '#fff', background: '#b91c1c', borderRadius: 4, padding: '6px 8px', fontWeight: 700 }}>
          {unboundText(t, entry.warning)}
        </p>
      )}
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
            <PinFields t={t} who="boss" choices={choices} pin={form.pins.boss} errorId={err.boss ? 'open-err-boss' : undefined} onChange={p => set({ pins: { ...form.pins, boss: p } })} />
            <Err id="open-err-boss">{err.boss}</Err>
            <PinFields t={t} who="verifier" choices={choices} pin={form.pins.verifier} errorId={err.verifier ? 'open-err-verifier' : undefined} onChange={p => set({ pins: { ...form.pins, verifier: p } })} />
            <Err id="open-err-verifier">{err.verifier}</Err>
            <Pick t={t} label={t('openStart.judging')} value={form.pins.judging} options={JUDGING_POLICIES} errorId={err.judging ? 'open-err-judging' : undefined} onChange={v => set({ pins: { ...form.pins, judging: v } })} />
            <Err id="open-err-judging">{err.judging}</Err>
          </section>

          <section style={box}>
            <label style={small}><input type="checkbox" checked={form.perAsk.enabled} onChange={e => perAsk({ enabled: e.target.checked })} /> {t('openStart.perAskOn')}</label>
            {!form.perAsk.enabled && <span style={small}>{t('openStart.perAskDeny')}</span>}
            {form.perAsk.enabled && (
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }} aria-describedby={err.perAsk ? 'open-err-perask' : undefined}>
                <Pick t={t} label={t('openStart.askSpend')} value={form.perAsk.spendUsd} options={PER_ASK_CHOICES.spendUsd} onChange={v => perAsk({ spendUsd: v })} />
                <Pick t={t} label={t('openStart.askTasks')} value={form.perAsk.tasks} options={PER_ASK_CHOICES.tasks} onChange={v => perAsk({ tasks: v })} />
                <Pick t={t} label={t('openStart.askDepth')} value={form.perAsk.depth} options={PER_ASK_CHOICES.depth} onChange={v => perAsk({ depth: v })} />
                <Pick t={t} label={t('openStart.askRunSpend')} value={form.perAsk.runSpendUsd} options={PER_ASK_CHOICES.runSpendUsd} onChange={v => perAsk({ runSpendUsd: v })} />
                <Pick t={t} label={t('openStart.askRunTasks')} value={form.perAsk.runTasks} options={PER_ASK_CHOICES.runTasks} onChange={v => perAsk({ runTasks: v })} />
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
          {review && <OpenStartReview key={review.preview.start_token} charter={review.charter} preview={review.preview} getChat={getChat} onChat={onChat} onStarted={r => { setDone(true); onStarted?.(r) }} onUnknown={() => setMayExist(true)} />}
        </fieldset>
      )}
    </div>
  )
}
