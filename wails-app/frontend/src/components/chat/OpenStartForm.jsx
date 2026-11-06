import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { openStartApi } from '../../lib/openStartApi.js'
import { ASK_ACCESS, LIMIT_KEYS, LIMIT_PRESETS, EVERYTHING_LINES, LINES, buildCharter, emptyForm } from '../../lib/openCharter.js'
import { OpenStartReview } from './OpenStartReview.jsx'

// The entry screen and the start form of the open-ended chat mode. The CLI decides what can start (the probe), what the
// charter means (render) and whether the typed phrase is right (start); this shows the plain statements the person
// must read, builds the charter from the form, and hands the phrase they write to the CLI. bossSession is the id of the
// boss session the chat flow creates; until it is given the run cannot be started from here.

const mono = 'var(--font-mono)'
const box = { border: '1px solid var(--border)', borderRadius: 6, padding: 10, display: 'flex', flexDirection: 'column', gap: 6 }
const small = { fontFamily: mono, fontSize: 11, color: 'var(--text-muted)' }
const field = { fontFamily: mono, fontSize: 11 }

function Text({ id, label, value, onChange, area, ...rest }) {
  const Tag = area ? 'textarea' : 'input'
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 2, ...small }}>
      {label}
      <Tag id={id} aria-label={label} value={value} onChange={e => onChange(e.target.value)} style={field} {...rest} />
    </label>
  )
}

const PinFields = ({ t, who, pin, onChange }) => (
  <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
    {['provider', 'model', 'version'].map(k => (
      <Text key={k} label={t(`openStart.${who}${k[0].toUpperCase()}${k.slice(1)}`)} value={pin[k]} onChange={v => onChange({ ...pin, [k]: v })} />
    ))}
  </div>
)

export function OpenStartForm({ bossSession = null, onStarted }) {
  const { t } = useTranslation()
  const [entry, setEntry] = useState(null)
  const [form, setForm] = useState(emptyForm)
  const [errors, setErrors] = useState({})
  const [review, setReview] = useState(null) // { charter, preview } once the CLI has rendered it
  const [reviewError, setReviewError] = useState('')
  const [busy, setBusy] = useState(false)

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
        <>
          <section style={box}>
            <Text label={t('openStart.goal')} value={form.goal} onChange={v => set({ goal: v })} area rows={3} />
            {err.goal && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.goal}</span>}
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
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ criteria: criteria.filter((_, j) => j !== i) })}>{t('openStart.remove')}</button>
              </div>
            ))}
            <div style={{ display: 'flex', gap: 6 }}>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ criteria: [...criteria, { kind: 'command', command: '', expected: '' }] })}>{t('openStart.addCommand')}</button>
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => set({ criteria: [...criteria, { kind: 'rubric', text: '' }] })}>{t('openStart.addRubric')}</button>
            </div>
            {err.criteria && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.criteria}</span>}
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
                <Text key={k} label={t(`openStart.limit.${k}`)} value={String(form.limits[k])} onChange={v => set({ limitsPreset: 'custom', limits: { ...form.limits, [k]: v } })} />
              ))}
            </div>
            {err.limits && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.limits}</span>}
          </section>

          <fieldset style={box}>
            <legend style={small}>{t('openStart.budget')}</legend>
            <label style={small}><input type="radio" name="open-budget" checked={form.budget.choice === 'unlimited'} onChange={() => set({ budget: { choice: 'unlimited', usd: '' } })} /> {t('openStart.budgetUnlimited')}</label>
            <label style={small}><input type="radio" name="open-budget" checked={form.budget.choice === 'fixed'} onChange={() => set({ budget: { ...form.budget, choice: 'fixed' } })} /> {t('openStart.budgetFixed')}</label>
            {form.budget.choice === 'fixed' && <Text label={t('openStart.budgetUsd')} value={form.budget.usd} onChange={v => set({ budget: { choice: 'fixed', usd: v } })} inputMode="decimal" />}
            {err.budget && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.budget}</span>}
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
            <PinFields t={t} who="boss" pin={form.pins.boss} onChange={p => set({ pins: { ...form.pins, boss: p } })} />
            {err.boss && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.boss}</span>}
            <PinFields t={t} who="verifier" pin={form.pins.verifier} onChange={p => set({ pins: { ...form.pins, verifier: p } })} />
            {err.verifier && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.verifier}</span>}
            <Text label={t('openStart.judging')} value={form.pins.judging} onChange={v => set({ pins: { ...form.pins, judging: v } })} />
            {err.judging && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.judging}</span>}
          </section>

          <section style={box}>
            <label style={small}><input type="checkbox" checked={form.perAsk.enabled} onChange={e => perAsk({ enabled: e.target.checked })} /> {t('openStart.perAskOn')}</label>
            {!form.perAsk.enabled && <span style={small}>{t('openStart.perAskDeny')}</span>}
            {form.perAsk.enabled && (
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
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
            {err.perAsk && <span role="alert" style={{ ...small, color: 'var(--red)' }}>{err.perAsk}</span>}
          </section>

          <button type="button" className="btn btn-sm" disabled={busy} onClick={doReview}>{t('openStart.review')}</button>
          {reviewError && <p role="alert" style={{ ...small, color: 'var(--red)' }}>{reviewError}</p>}
          {review && <OpenStartReview charter={review.charter} preview={review.preview} bossSession={bossSession} onStarted={onStarted} />}
        </>
      )}
    </div>
  )
}
