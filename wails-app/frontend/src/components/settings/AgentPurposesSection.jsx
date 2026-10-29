import { useState, useEffect, useCallback } from 'react'
import { agentPurposesApi, pairKey } from './agentPurposesApi.js'

// Agent purposes: for each kind of work agents do, the systems and models a
// parent may pick from when it starts a sub-agent. Only validated pairs can be
// chosen (a stale one still works; a failed or untested one is shown, not
// selectable). A purpose with nothing chosen uses every validated pair. The
// settings are `monoagentcli agent purposes` (app_agent_purposes.go); this
// window runs as you, so it is the one place they are changed.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const row = { display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }
const name = { fontFamily: 'var(--font-body)', fontSize: 12.5, color: 'var(--text)' }

const errMsg = (e) => String(e?.message || e || 'unknown error')
const RULES = [
  ['any', 'Any of these (picked by fit, track record and cost)'],
  ['order', 'In this order'],
  ['cheapest', 'The cheapest that meets the quality bar'],
]
const STATE_COLOR = { ready: 'var(--green)', stale: 'var(--amber)', failed: 'var(--red)' }
const STATE_TEXT = { ready: 'ready', stale: 'stale', failed: 'failed', untested: 'untested', unknown: 'no longer in the roster' }

const draftOf = (p) => ({
  pairs: (p.pairs || []).map(x => ({ ...x })),
  rule: p.rule || 'any',
  effort: p.effort || '',
  differentModel: !!p.different_from_author,
  budgetShare: String(p.budget_share_pct || 0),
  fallback: !!p.fallback,
})

function Chip({ pair, onRemove, extra }) {
  return (
    <span data-pair={pairKey(pair)} data-state={pair.state} style={{ ...row, gap: 6, border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '2px 8px' }}>
      <span aria-hidden style={{ width: 7, height: 7, borderRadius: '50%', background: STATE_COLOR[pair.state] || 'var(--text-muted)' }} />
      <span style={{ ...name, fontSize: 11.5 }}>{pair.label || pairKey(pair)}</span>
      <span style={hint}>{STATE_TEXT[pair.state] || pair.state}</span>
      {extra}
      {onRemove && (
        <button type="button" className="btn btn-ghost btn-sm" aria-label={`Remove ${pair.label || pairKey(pair)}`} onClick={onRemove}>×</button>
      )}
    </span>
  )
}

function PurposeEditor({ purpose, available, busy, onSave, onReset }) {
  const [d, setD] = useState(() => draftOf(purpose))
  useEffect(() => { setD(draftOf(purpose)) }, [purpose])
  const chosen = new Set(d.pairs.map(pairKey))
  const blocked = d.pairs.filter(p => !p.selectable)
  const share = d.budgetShare.trim()
  const shareOk = /^\d{1,3}$/.test(share) && Number(share) <= 100
  const dirty = JSON.stringify(d) !== JSON.stringify(draftOf(purpose))
  const toggle = (p) => setD(s => chosen.has(pairKey(p))
    ? { ...s, pairs: s.pairs.filter(x => pairKey(x) !== pairKey(p)) }
    : { ...s, pairs: [...s.pairs, p] })
  const move = (i, by) => setD(s => {
    const next = s.pairs.slice(); const j = i + by
    if (j < 0 || j >= next.length) return s
    ;[next[i], next[j]] = [next[j], next[i]]
    return { ...s, pairs: next }
  })
  const submit = (e) => {
    e.preventDefault()
    if (!shareOk || blocked.length) return
    onSave(purpose.id, {
      pairs: d.pairs.map(pairKey), rule: d.rule, effort: d.effort.trim(),
      differentModel: d.differentModel, budgetSharePct: Number(share), fallback: d.fallback,
    })
  }
  const offered = available.filter(p => p.selectable)
  return (
    <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: 10, paddingTop: 8 }} aria-label={`${purpose.title} settings`}>
      <div style={label}>Chosen systems and models</div>
      {d.pairs.length === 0 && <div style={hint}>Nothing chosen: every validated pair is used.</div>}
      {d.pairs.map((p, i) => (
        <div key={pairKey(p)} style={row}>
          <Chip pair={p} onRemove={() => toggle(p)} />
          {d.rule === 'order' && (
            <>
              <button type="button" className="btn btn-ghost btn-sm" aria-label={`Move ${p.label || pairKey(p)} up`} disabled={i === 0} onClick={() => move(i, -1)}>↑</button>
              <button type="button" className="btn btn-ghost btn-sm" aria-label={`Move ${p.label || pairKey(p)} down`} disabled={i === d.pairs.length - 1} onClick={() => move(i, 1)}>↓</button>
            </>
          )}
        </div>
      ))}
      {blocked.length > 0 && (
        <div style={errText} role="alert">
          {blocked.map(p => p.label || pairKey(p)).join(', ')} cannot be used now. Remove {blocked.length === 1 ? 'it' : 'them'} to save.
        </div>
      )}
      <div style={label}>Choose from the validated roster</div>
      {offered.length === 0 && <div style={hint}>No validated models yet. Validate some on the Agents page.</div>}
      <div style={row}>
        {offered.map(p => (
          <label key={pairKey(p)} data-offer={pairKey(p)} style={{ ...row, gap: 6, ...name, fontSize: 11.5 }}>
            <input type="checkbox" checked={chosen.has(pairKey(p))} onChange={() => toggle(p)} disabled={busy} />
            {p.label || pairKey(p)}
            <span style={hint}>{STATE_TEXT[p.state] || p.state}</span>
          </label>
        ))}
      </div>
      {available.some(p => !p.selectable) && (
        <div style={hint}>
          Not selectable: {available.filter(p => !p.selectable).map(p => `${p.label || pairKey(p)} (${STATE_TEXT[p.state] || p.state})`).join(', ')}.
        </div>
      )}

      <div style={row}>
        <label htmlFor={`rule-${purpose.id}`} style={name}>Selection rule</label>
        <select id={`rule-${purpose.id}`} value={d.rule} disabled={busy} onChange={e => setD(s => ({ ...s, rule: e.target.value }))}>
          {RULES.map(([id, text]) => <option key={id} value={id}>{text}</option>)}
        </select>
      </div>
      <div style={row}>
        <label htmlFor={`effort-${purpose.id}`} style={name}>Effort</label>
        <input id={`effort-${purpose.id}`} className="form-input" placeholder="automatic" spellCheck={false} value={d.effort} disabled={busy}
          onChange={e => setD(s => ({ ...s, effort: e.target.value }))} style={{ width: 120, fontFamily: mono, fontSize: 12 }} />
        <label htmlFor={`share-${purpose.id}`} style={name}>Budget share (%)</label>
        <input id={`share-${purpose.id}`} className="form-input" inputMode="numeric" value={d.budgetShare} disabled={busy}
          onChange={e => setD(s => ({ ...s, budgetShare: e.target.value }))} style={{ width: 70, fontFamily: mono, fontSize: 12 }} />
      </div>
      <label style={{ ...row, gap: 6, ...name }}>
        <input type="checkbox" checked={d.differentModel} disabled={busy} onChange={e => setD(s => ({ ...s, differentModel: e.target.checked }))} />
        Use a different model than the one that wrote the work
      </label>
      <label style={{ ...row, gap: 6, ...name }}>
        <input type="checkbox" checked={d.fallback} disabled={busy} onChange={e => setD(s => ({ ...s, fallback: e.target.checked }))} />
        If every chosen pair is unavailable, allow any validated pair
      </label>
      <div style={row}>
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy || !dirty || !shareOk || blocked.length > 0}>Save</button>
        <button type="button" className="btn btn-ghost btn-sm" disabled={busy || !purpose.configured} onClick={() => onReset(purpose.id)}>Reset to default</button>
        {!shareOk && <span style={errText}>The budget share is a whole number from 0 to 100.</span>}
      </div>
    </form>
  )
}

export default function AgentPurposesSection() {
  const [doc, setDoc] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(null)

  const load = useCallback(async () => {
    try { setDoc(await agentPurposesApi.list()); setLoadErr('') } catch (e) { setLoadErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  const run = async (fn) => {
    setBusy(true); setErr('')
    try { await fn(); await load() } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const save = (id, s) => run(() => agentPurposesApi.set(id, s))
  const reset = (id) => run(() => agentPurposesApi.reset(id))

  if (!doc) {
    return (
      <div style={card} data-testid="agent-purposes">
        {loadErr ? <div style={errText} role="alert">{loadErr}</div> : <div style={hint}>Loading…</div>}
      </div>
    )
  }
  const purposes = Array.isArray(doc.purposes) ? doc.purposes : []
  const available = Array.isArray(doc.available) ? doc.available : []

  return (
    <div style={card} data-testid="agent-purposes">
      <div style={label}>Agent purposes</div>
      <div style={hint}>
        For each kind of work, choose the systems and models an agent may use when it starts a sub-agent. A purpose with
        nothing chosen uses every validated pair. Only validated models can be chosen. Only you change this; an agent
        cannot pick outside the set or edit it.
      </div>
      {err && <div style={errText} role="alert">{err}</div>}
      {purposes.map(p => (
        <div key={p.id} data-purpose={p.id} style={{ borderTop: '1px solid var(--border)', paddingTop: 10 }}>
          <button type="button" className="btn btn-ghost btn-sm" aria-expanded={open === p.id}
            onClick={() => setOpen(open === p.id ? null : p.id)} style={{ ...row, width: '100%', justifyContent: 'space-between' }}>
            <span style={name}>{p.title}</span>
            <span style={hint}>
              {p.configured ? `${(p.pairs || []).length} chosen · ${p.rule}` : 'all validated pairs'}
            </span>
          </button>
          {p.description && <div style={hint}>{p.description}</div>}
          {open === p.id && <PurposeEditor purpose={p} available={available} busy={busy} onSave={save} onReset={reset} />}
        </div>
      ))}
      <div style={row}>
        <button type="button" className="btn btn-ghost btn-sm" disabled={busy || !purposes.some(p => p.configured)}
          onClick={() => run(() => agentPurposesApi.resetAll())}>Reset all to default</button>
      </div>
    </div>
  )
}
