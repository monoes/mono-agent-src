import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { JevAddProvider } from '../../wailsjs/go/main/App'
import { mono, label, hint, errText, okText, input, testError } from './jevShared.jsx'

// A preset fills the kind, the base URL, a hosted service's model (and a name
// when the name field is empty or still the last preset's). The key is sent
// once, on Add, and the field is cleared when the provider is saved.
export const PRESETS = [
  { id: 'openai', kind: 'openai', base: 'https://api.openai.com/v1', model: 'gpt-4o-mini', name: 'openai', needsKey: true },
  { id: 'ollama', kind: 'openai', base: 'http://localhost:11434/v1', name: 'ollama', local: true },
  { id: 'openrouter', kind: 'openai', base: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini', name: 'openrouter', needsKey: true },
  { id: 'llamacpp', kind: 'openai', base: 'http://localhost:8080/v1', name: 'llamacpp', local: true },
  { id: 'vllm', kind: 'openai', base: 'http://localhost:8000/v1', name: 'vllm', local: true },
  { id: 'lmstudio', kind: 'openai', base: 'http://localhost:1234/v1', name: 'lmstudio', local: true },
  { id: 'anthropic', kind: 'anthropic', base: 'https://api.anthropic.com', model: 'claude-3-5-haiku-latest', name: 'anthropic', needsKey: true },
  { id: 'hf', kind: 'hf-zeroshot', base: '', name: 'hf-zeroshot' },
  { id: 'custom', kind: '', base: '', name: '' },
]

const NAME_RE = /^[a-z0-9][a-z0-9-]{0,31}$/

// parsePrice reads an optional price field: '' is 0 (no price), anything
// else must be a finite number of 0 or more (null = invalid).
export function parsePrice(raw) {
  const s = String(raw ?? '').trim()
  if (s === '') return 0
  const v = Number(s)
  return Number.isFinite(v) && v >= 0 ? v : null
}

function Field({ id, text, children }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: '1 1 200px', minWidth: 0 }}>
      <label htmlFor={id} style={label}>{text}</label>
      {children}
    </div>
  )
}

export default function JevAddProviderForm({ kinds, busy, run, reload, onClose }) {
  const { t } = useTranslation()
  const [presetId, setPresetId] = useState('ollama')
  const preset = PRESETS.find(p => p.id === presetId) || PRESETS[PRESETS.length - 1]
  const [name, setName] = useState(preset.name)
  const [kind, setKind] = useState(preset.kind || kinds[0] || '')
  const [base, setBase] = useState(preset.base)
  const [model, setModel] = useState('')
  const [key, setKey] = useState('')
  const [priceIn, setPriceIn] = useState('')
  const [priceOut, setPriceOut] = useState('')
  const [err, setErr] = useState('')
  const [result, setResult] = useState(null)

  const kindOptions = kind && !kinds.includes(kind) ? [kind, ...kinds] : kinds
  const pick = (id) => {
    const next = PRESETS.find(p => p.id === id)
    if (!next) return
    if (!name || name === preset.name) setName(next.name)
    setKind(next.kind || kinds[0] || '')
    setBase(next.base)
    // A hosted preset's model is filled in, so what is shown is what is sent
    // (an empty model means the adapter's default).
    if (!model || model === (preset.needsKey ? preset.model : '')) setModel(next.needsKey ? next.model : '')
    setPresetId(id)
  }
  const modelPlaceholder = preset.local ? t('settings.jev.providers.modelLocalPlaceholder') : ''
  const usdIn = parsePrice(priceIn)
  const usdOut = parsePrice(priceOut)
  const valid = NAME_RE.test(name) && !!kind && !!base.trim() && (!preset.needsKey || !!key.trim()) &&
    usdIn != null && usdOut != null

  const submit = (e) => {
    e.preventDefault()
    if (!valid) return
    setErr(''); setResult(null)
    run('provider:add', async () => {
      const p = await JevAddProvider({
        name, kind, base_url: base.trim(), model: model.trim(), api_key: key,
        usd_per_mtok_in: usdIn, usd_per_mtok_out: usdOut, no_probe: false,
      })
      setKey('')
      setResult(p || { name })
      setName(''); setModel(''); setPriceIn(''); setPriceOut('')
      await reload()
    }, setErr)
  }

  const id = (f) => `jev-add-${f}`
  let resultText = null
  if (result) {
    if (!result.probe) resultText = <div style={okText}>{t('settings.jev.providers.added', { name: result.name })}</div>
    else if (result.probe.ok) resultText = <div style={okText}>{t('settings.jev.providers.addedProbeOk', { name: result.name })}</div>
    else resultText = <div style={errText}>{t('settings.jev.providers.addedProbeFailed', { name: result.name, error: testError(result.probe.error) })}</div>
  }

  return (
    <form data-testid="jev-add-provider" onSubmit={submit} aria-label={t('settings.jev.providers.addTitle')} style={{
      borderTop: '1px solid var(--border-dim)', paddingTop: 12, display: 'flex', flexDirection: 'column', gap: 10,
    }}>
      <span style={{ fontFamily: mono, fontSize: 11, fontWeight: 600, color: 'var(--text)' }}>{t('settings.jev.providers.addTitle')}</span>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
        <Field id={id('preset')} text={t('settings.jev.providers.preset')}>
          <select id={id('preset')} className="filter-select" value={presetId} disabled={busy} onChange={e => pick(e.target.value)}>
            {PRESETS.map(p => <option key={p.id} value={p.id}>{t(`settings.jev.providers.presets.${p.id}`)}</option>)}
          </select>
        </Field>
        <Field id={id('kind')} text={t('settings.jev.providers.kind')}>
          <select id={id('kind')} className="filter-select" value={kind} disabled={busy} onChange={e => setKind(e.target.value)}>
            {kindOptions.map(k => <option key={k} value={k}>{k}</option>)}
          </select>
        </Field>
      </div>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
        <Field id={id('name')} text={t('settings.jev.providers.name')}>
          <input id={id('name')} value={name} disabled={busy} spellCheck={false} autoComplete="off" maxLength={32}
            aria-describedby={id('name-hint')} aria-invalid={!!name && !NAME_RE.test(name)}
            onChange={e => setName(e.target.value)} style={input} />
          <span id={id('name-hint')} style={hint}>{t('settings.jev.providers.nameHint')}</span>
        </Field>
        <Field id={id('model')} text={t('settings.jev.providers.model')}>
          <input id={id('model')} value={model} disabled={busy} spellCheck={false} autoComplete="off"
            placeholder={modelPlaceholder} onChange={e => setModel(e.target.value)} style={input} />
        </Field>
      </div>
      <Field id={id('base')} text={t('settings.jev.providers.baseURL')}>
        <input id={id('base')} value={base} disabled={busy} spellCheck={false} autoComplete="off" type="url"
          placeholder={presetId === 'hf' ? t('settings.jev.providers.baseURLHfPlaceholder') : 'https://…'}
          onChange={e => setBase(e.target.value)} style={input} />
      </Field>
      <Field id={id('key')} text={preset.needsKey ? t('settings.jev.providers.apiKey') : t('settings.jev.providers.apiKeyOptional')}>
        <input id={id('key')} type="password" autoComplete="off" spellCheck={false} value={key} disabled={busy}
          placeholder={preset.local ? t('settings.jev.providers.keyLocalPlaceholder') : t('settings.jev.providers.keyPlaceholder')}
          onChange={e => setKey(e.target.value)} style={input} />
      </Field>
      <details data-testid="jev-add-pricing">
        <summary style={{ ...label, cursor: 'pointer' }}>{t('settings.jev.providers.pricing')}</summary>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 8 }}>
          <span id={id('price-hint')} style={hint}>{t('settings.jev.providers.pricingHint')}</span>
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
            {[['price-in', 'priceIn', priceIn, setPriceIn, usdIn], ['price-out', 'priceOut', priceOut, setPriceOut, usdOut]].map(([f, k, v, set, parsed]) => (
              <Field key={f} id={id(f)} text={t(`settings.jev.providers.${k}`)}>
                <input id={id(f)} type="number" min={0} step="any" inputMode="decimal" value={v} disabled={busy}
                  aria-describedby={parsed == null ? `${id('price-hint')} ${id(f)}-err` : id('price-hint')}
                  aria-invalid={parsed == null} onChange={e => set(e.target.value)} style={input} />
                {parsed == null && <span id={`${id(f)}-err`} style={errText}>{t('settings.jev.providers.priceInvalid')}</span>}
              </Field>
            ))}
          </div>
        </div>
      </details>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
        <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onClose}>{t('settings.jev.providers.cancel')}</button>
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy || !valid}>
          {busy ? t('settings.jev.providers.adding') : t('settings.jev.providers.add')}
        </button>
      </div>
      {err && <div role="alert" style={errText}>{err}</div>}
      <div aria-live="polite" data-testid="jev-add-result">{resultText}</div>
    </form>
  )
}
