import { useState, useEffect, useCallback } from 'react'
import {
  JevListProviders, JevUseProvider, JevSetProviderEndpoint, JevSetProviderKey, JevRemoveProviderKey,
} from '../../wailsjs/go/main/App'
import { mono, label, hint, errText, input, errMsg } from './jevShared.jsx'

// Decision providers: every vendor of typed-question decision models, one of
// which answers all Jev surfaces. Only /v1/systemone providers can be selected;
// the rest are listed with "no adapter yet". Everything goes through
// `monoagentcli jev provider …`; a key travels on stdin only.
export default function DecisionProvidersBlock({ onChanged }) {
  const [data, setData] = useState(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [open, setOpen] = useState('')
  const [form, setForm] = useState({ base: '', model: '', key: '' })

  const load = useCallback(async () => {
    try { setData(await JevListProviders()); setErr('') } catch (e) { setErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  const run = async (what, fn) => {
    setBusy(what); setErr('')
    try { await fn(); await load(); if (onChanged) await onChanged() } catch (e) { setErr(errMsg(e)) } finally { setBusy('') }
  }
  const edit = (p) => { setOpen(open === p.id ? '' : p.id); setForm({ base: p.base_url || '', model: p.model || '', key: '' }) }
  const save = (p) => run(`save:${p.id}`, async () => {
    await JevSetProviderEndpoint(p.id, form.base, form.model)
    if (form.key.trim()) await JevSetProviderKey(p.id, form.key)
    setForm(f => ({ ...f, key: '' }))
  })

  if (!data) return <div style={hint}>{err ? `Couldn't read decision providers: ${err}` : 'Loading decision providers…'}</div>

  const stateOf = (p) => {
    if (!p.supported) return 'no adapter yet'
    if (!p.base_url) return 'needs base URL'
    if (!p.has_key && !p.key_optional) return 'needs key'
    return 'ready'
  }

  return (
    <div data-testid="decision-providers" style={{ marginTop: 14 }}>
      <div style={label}>Decision providers</div>
      <div style={{ ...hint, margin: '4px 0 8px' }}>
        These answer pick-one, yes/no and score questions, and never write text. One provider is used for every Jev
        feature. Local models work through a custom /v1/systemone endpoint.
      </div>
      {data.providers.map(p => (
        <div key={p.id} data-testid={`provider-${p.id}`} style={{ borderTop: '1px solid var(--border)', padding: '8px 0' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <input
              type="radio" name="decision-provider" aria-label={`Use ${p.name}`}
              checked={p.selected} disabled={!p.supported || !!busy}
              onChange={() => run(`use:${p.id}`, () => JevUseProvider(p.id))}
            />
            <span style={{ fontFamily: mono, fontSize: 12, fontWeight: 700, color: 'var(--text)' }}>{p.name}</span>
            <span style={{ ...hint, flex: 1 }}>{p.models?.join(', ')}</span>
            <span style={{ fontFamily: mono, fontSize: 10.5, color: stateOf(p) === 'ready' ? 'var(--green-neon)' : 'var(--text-muted)' }}>
              {stateOf(p)}
            </span>
            {p.supported && p.id !== 'typesafe' && (
              <button className="btn btn-secondary btn-sm" onClick={() => edit(p)}>{open === p.id ? 'Close' : 'Set up'}</button>
            )}
          </div>
          <div style={hint}>{p.note}</div>
          {open === p.id && (
            <div style={{ display: 'grid', gap: 6, marginTop: 8 }}>
              <input style={input} placeholder="Base URL (https://…, or http://127.0.0.1:… locally)" value={form.base}
                onChange={e => setForm({ ...form, base: e.target.value })} aria-label={`${p.name} base URL`} />
              <input style={input} placeholder="Model" value={form.model}
                onChange={e => setForm({ ...form, model: e.target.value })} aria-label={`${p.name} model`} />
              <input style={input} type="password" autoComplete="off"
                placeholder={p.key_optional ? 'API key (optional)' : p.has_key ? 'API key (saved; paste to replace)' : 'API key'}
                value={form.key} onChange={e => setForm({ ...form, key: e.target.value })} aria-label={`${p.name} API key`} />
              <div style={{ display: 'flex', gap: 8 }}>
                <button className="btn btn-primary btn-sm" disabled={!!busy} onClick={() => save(p)}>Save</button>
                {p.has_key && (
                  <button className="btn btn-secondary btn-sm" disabled={!!busy}
                    onClick={() => run(`rm:${p.id}`, () => JevRemoveProviderKey(p.id))}>Remove key</button>
                )}
              </div>
            </div>
          )}
        </div>
      ))}
      {err && <div role="alert" style={errText}>{err}</div>}
    </div>
  )
}
