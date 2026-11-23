import { useState, useEffect, useCallback } from 'react'
import { X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { aitaskBudgetApi, money } from './aitaskBudgetApi.js'
import { confirm } from '../ConfirmDialog.jsx'

// AI task budgets: spend caps for the tasks the Send to coder buttons and
// workflows hand to the coder. A task runs under the lowest cap that applies
// to it (all tasks, its kind, its conversation, its role) and never above the
// coder budget. 0 means no spend, never unlimited, and with no cap at all a
// task does not run. The caps and what they have used are
// `monoagentcli aitask budget` (app_aitask_budget.go); this window runs as you,
// so it is the one place a cap is raised.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const row = { display: 'flex', alignItems: 'center', gap: 12, borderTop: '1px solid var(--border)', paddingTop: 10, flexWrap: 'wrap' }
const name = { fontFamily: 'var(--font-body)', fontSize: 12.5, color: 'var(--text)' }

const errMsg = (e) => String(e?.message || e || 'unknown error')
const AGENT_SCOPES = { conversation: 'Conversation', role: 'Role' }

// CapForm adds or changes one cap: an optional key, an amount in USD, and a
// button. scopes is a list of [scope, label] to choose from, or null for one fixed scope.
// Plain decimals only: Number() would also take '1e2', '0x10' and ' 5 '.
export const PLAIN_DECIMAL = /^(\d+\.?\d*|\.\d+)$/

function CapForm({ scope, scopes, keyLabel, busy, onSave, button, visibleLabel }) {
  const [pick, setPick] = useState(scopes ? scopes[0][0] : scope)
  const [key, setKey] = useState('')
  const [usd, setUsd] = useState('')
  const needsKey = keyLabel != null
  const n = Number(usd)
  const valid = PLAIN_DECIMAL.test(usd.trim()) && Number.isFinite(n) && (!needsKey || key.trim() !== '')
  const submit = async (e) => {
    e.preventDefault()
    if (!valid) return
    if (await onSave(pick, needsKey ? key.trim() : '', n)) { setKey(''); setUsd('') }
  }
  return (
    <form onSubmit={submit} style={row}>
      {scopes && (
        <select aria-label="Kind of agent cap" value={pick} onChange={e => setPick(e.target.value)} disabled={busy}>
          {scopes.map(([id, text]) => <option key={id} value={id}>{text}</option>)}
        </select>
      )}
      {needsKey && (
        <input className="form-input" aria-label={keyLabel} placeholder={keyLabel} spellCheck={false}
          value={key} onChange={e => setKey(e.target.value)} disabled={busy}
          style={{ flex: '1 1 140px', minWidth: 0, fontFamily: mono, fontSize: 12 }} />
      )}
      {visibleLabel && <label htmlFor={`cap-usd-${pick}`} style={{ ...name, fontSize: 12 }}>{visibleLabel}</label>}
      <input id={`cap-usd-${pick}`} className="form-input" aria-label={`${keyLabel || 'Global'} cap in USD`} placeholder="USD" inputMode="decimal"
        value={usd} onChange={e => setUsd(e.target.value)} disabled={busy} style={{ width: 90, maxWidth: '100%', fontFamily: mono, fontSize: 12 }} />
      <button type="submit" className="btn btn-primary btn-sm" disabled={busy || !valid}>{button}</button>
    </form>
  )
}

function CapRows({ caps, busy, onClear }) {
  if (caps.length === 0) return <div style={hint}>None set.</div>
  return caps.map(c => (
    <div key={`${c.scope}:${c.key}`} data-cap={`${c.scope}:${c.key}`} style={row}>
      <span style={{ ...name, fontFamily: mono, fontSize: 11.5, flex: 1, minWidth: 0, wordBreak: 'break-all' }}>
        {AGENT_SCOPES[c.scope] ? `${AGENT_SCOPES[c.scope]} ` : ''}{c.key}
      </span>
      <span style={name}>{money(c.usedUsd)} of {money(c.capUsd)}</span>
      {c.capUsd === 0 && <span style={hint}>no spend</span>}
      <button type="button" className="btn btn-ghost btn-sm" disabled={busy}
        aria-label={`Remove the cap for ${c.key || c.scope}`} title="Remove this cap" onClick={() => onClear(c)}>
        <X size={12} />
      </button>
    </div>
  ))
}

export default function AiTaskBudgetsSection() {
  const { t } = useTranslation()
  const [doc, setDoc] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try { setDoc(await aitaskBudgetApi.list()); setLoadErr('') } catch (e) { setLoadErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  const run = async (fn) => {
    setBusy(true); setErr('')
    try { await fn(); await load(); return true } catch (e) { setErr(errMsg(e)); return false } finally { setBusy(false) }
  }
  const save = (scope, key, usd) => run(() => aitaskBudgetApi.set(scope, key, usd))
  const clear = async (c) => {
    if (!(await confirm(t('budgets.removeBody', { name: c.key || c.scope }), { danger: true, confirmLabel: t('budgets.removeLabel'), cancelLabel: t('budgets.keep') }))) return
    return run(() => aitaskBudgetApi.clear(c.scope, c.key))
  }

  if (!doc) {
    return (
      <div style={card} data-testid="aitask-budgets">
        {loadErr ? <div style={errText} role="alert">{loadErr}</div> : <div style={hint}>Loading…</div>}
      </div>
    )
  }
  const caps = Array.isArray(doc.caps) ? doc.caps : []
  const global = caps.find(c => c.scope === 'global')
  const ofScope = (...scopes) => caps.filter(c => scopes.includes(c.scope))

  return (
    <div style={card} data-testid="aitask-budgets">
      <div style={label}>AI task budgets</div>
      <div style={hint}>
        Spend caps for tasks handed to the coder. A task runs under the lowest cap that applies to it and never above
        the coder budget{doc.operatorUsd > 0 ? ` (${money(doc.operatorUsd)} per message)` : ' (not set)'}. A cap of 0 means no
        spend, never unlimited, and with no cap at all a task does not run. Tasks and the agents they start count
        against the same caps. Only you raise a cap; an agent can only lower its own.
      </div>
      {err && <div style={errText} role="alert">{err}</div>}

      <div style={{ ...label, marginTop: 6 }}>All tasks</div>
      <div data-cap="global:" style={{ ...name, display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
        {global ? <span>{money(global.usedUsd)} of {money(global.capUsd)}</span> : <span style={hint}>No global cap.</span>}
        {global && <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={() => clear(global)}>Remove</button>}
      </div>
      <CapForm scope="global" busy={busy} onSave={save} button="Set cap" visibleLabel="Cap for all tasks (USD)" />

      <div style={{ ...label, marginTop: 6 }}>Per kind of task</div>
      <CapRows caps={ofScope('kind')} busy={busy} onClear={clear} />
      <CapForm scope="kind" keyLabel="Kind (e.g. generic)" busy={busy} onSave={save} button="Add cap" />

      <div style={{ ...label, marginTop: 6 }}>Per agent</div>
      <div style={hint}>A conversation is one coder chat, with the workers it starts; a role is a kind of worker.</div>
      <CapRows caps={ofScope('conversation', 'role')} busy={busy} onClear={clear} />
      <CapForm scopes={Object.entries(AGENT_SCOPES)} keyLabel="Conversation id or role" busy={busy} onSave={save} button="Add cap" />
    </div>
  )
}
