import { useState, useEffect, useCallback } from 'react'
import { Code2, FolderOpen } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import FixMonomindButton from '../FixMonomindButton.jsx'
import { missingText, monomindNeedsFix, monomindUnusable, coderReady, coderRuntimes, runtimeReadiness } from '../chat/useCoderMode.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import CoderDefaultAccess from './CoderDefaultAccess.jsx'

// Coder mode (#203): a chat mode in which a coding agent (claude, codex,
// opencode, …) runs with full access inside one folder. Off by default; turning it on asks for an explicit
// risk confirmation, which `coder enable --yes-i-understand` records. The
// workspace root and per-turn defaults are `coder set`. Everything goes
// through the CLI (app_coder.go).

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const warnText = { fontFamily: mono, fontSize: 10.5, color: 'var(--yellow)', lineHeight: 1.5 }
const input = {
  background: 'var(--elevated)', border: '1px solid var(--border)', color: 'var(--text)',
  borderRadius: 'var(--radius)', fontFamily: mono, fontSize: 12, padding: '6px 10px', minWidth: 0,
}
const fieldRow = { display: 'flex', flexDirection: 'column', gap: 4 }

const errMsg = (e) => String(e?.message || e || 'unknown error')

export const CODER_RISK_TITLE = 'Turn on Coder mode?'

// The confirm dialog's body: what the agent can do once this is on.
export function CoderRiskText() {
  return (
    <div data-testid="coder-risk-text">
      <p style={{ margin: '0 0 8px' }}>In a Coder chat, the coding agent you pick (Claude Code, Codex, OpenCode, …) works on your computer with no approval prompts:</p>
      <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <li>It can run any command, read and change any file your user account can, and install software.</li>
        <li>Web pages and files in the chosen folder can contain instructions that steer it. Only point it at folders and repos you trust.</li>
        <li>It loads that agent&apos;s normal setup: instructions files (CLAUDE.md, AGENTS.md, GEMINI.md), skills, hooks and MCP servers.</li>
      </ul>
    </div>
  )
}

// formFrom seeds the editable fields from a status.
function formFrom(st) {
  return {
    workspaceRoot: st?.workspaceRoot || '',
    maxTurns: st?.maxTurns ? String(st.maxTurns) : '',
    timeout: st?.timeout || '',
    budgetUsd: st?.budgetUsd ? String(st.budgetUsd) : '',
  }
}

export default function CoderModeSection() {
  const [status, setStatus] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [form, setForm] = useState(formFrom(null))
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')

  const apply = (st) => { setStatus(st); setForm(formFrom(st)) }

  const load = useCallback(async () => {
    try { apply(await api.coderStatus()); setLoadErr('') } catch (e) { setLoadErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  const run = async (what, fn) => {
    setBusy(what); setErr(''); setNote('')
    try { await fn() } catch (e) { setErr(errMsg(e)) } finally { setBusy('') }
  }

  const toggle = async (on) => {
    if (on) {
      const ok = await confirm(<CoderRiskText />, { title: CODER_RISK_TITLE, confirmLabel: 'Turn on Coder mode', danger: true })
      if (!ok) return
      await run('enable', async () => apply(await api.coderEnable()))
    } else {
      await run('disable', async () => apply(await api.coderDisable()))
    }
  }

  const browse = async () => {
    const dir = await Promise.resolve(api.pickCoderFolder()).catch(() => '')
    if (dir) setForm(f => ({ ...f, workspaceRoot: dir }))
  }

  const save = () => run('save', async () => {
    const maxTurns = parseInt(form.maxTurns, 10)
    const budget = form.budgetUsd.trim() === '' ? 0 : Number(form.budgetUsd)
    if (form.maxTurns && !(maxTurns > 0)) throw new Error('Max turns must be a positive number')
    if (!(budget >= 0)) throw new Error('Budget must be a number of dollars, or empty for none')
    apply(await api.coderSet({ workspaceRoot: form.workspaceRoot.trim(), maxTurns: maxTurns || 0, timeout: form.timeout.trim(), budgetUsd: budget }))
    setNote('Saved. New coder chats use these settings.')
  })

  const set = (k) => (e) => setForm(f => ({ ...f, [k]: e.target.value }))
  const enabled = !!status?.enabled
  const disabled = !!busy || !status
  const ready = coderReady(status)
  // Per-runtime status (a monoagentcli with per-runtime coder mode): max
  // turns and the budget only bind on runtimes that report turns / cost,
  // and the budget field is hidden when none does.
  const perRuntime = Array.isArray(status?.runtimes) ? coderRuntimes(status) : null
  const limitNames = (key) => (perRuntime || []).filter(r => r.ready && r[key]).map(r => runtimeLabel(r.id))
  const showBudget = !perRuntime || limitNames('reportsCost').length > 0

  return (
    <div id="settings-coder-mode" data-testid="coder-mode-section" style={card}>
      <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, cursor: disabled ? 'default' : 'pointer' }}>
        <input
          type="checkbox" checked={enabled} disabled={disabled} aria-label="Coder mode"
          onChange={e => toggle(e.target.checked)}
          style={{ marginTop: 2, accentColor: '#00b4d8', flexShrink: 0 }}
        />
        <span style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 11, color: 'var(--text-secondary)' }}>
            <Code2 size={12} color="#00b4d8" /> Coder mode
            {busy === 'enable' && <span style={{ color: 'var(--text-muted)' }}>· turning on…</span>}
            {busy === 'disable' && <span style={{ color: 'var(--text-muted)' }}>· turning off…</span>}
          </span>
          <span style={hint}>
            Lets a chat run as a coding agent with access to your computer inside one folder, the coder root by default.
            When it is on and a runtime is ready, new chats start as coder chats (see the default access below).
            Off unless you turn it on.
          </span>
        </span>
      </label>

      {loadErr && <div role="alert" style={errText}>Coder mode is unavailable: {loadErr}</div>}
      {status && ready && (
        <div data-testid="coder-ready" style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)' }}>
          Ready{status.monomindVersion ? ` · monomind ${status.monomindVersion}` : ''}
          {perRuntime ? ` · ${perRuntime.filter(r => r.ready).map(r => runtimeLabel(r.id)).join(', ')}` : status.runtime ? ` · ${status.runtime}` : ''}
        </div>
      )}
      {perRuntime && perRuntime.length > 0 && (
        <ul data-testid="coder-runtimes" aria-label="Coding runtimes" style={{ margin: 0, paddingLeft: 16, fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', lineHeight: 1.6 }}>
          {perRuntime.map(r => (
            <li key={r.id} style={{ color: r.ready ? 'var(--text-secondary)' : undefined }}>
              {runtimeLabel(r.id)} · {runtimeReadiness(r)}
              {r.ready && r.toolActivity && r.toolActivity !== 'full' ? ` · tool calls: ${r.toolActivity}` : ''}
            </li>
          ))}
        </ul>
      )}
      {status && !ready && (
        <div data-testid="coder-needs-update" style={warnText}>
          Coder mode {missingText(status)}. You can turn it on now; Coder chats start once {monomindUnusable(status) ? 'monomind works' : status.missingCapabilities?.length || !perRuntime ? 'monomind is updated' : 'a runtime is ready'}.
          {monomindNeedsFix(status) && <> <FixMonomindButton onDone={load} /></>}
        </div>
      )}

      {status && <CoderDefaultAccess status={status} onSaved={apply} />}

      {status && (
        <form onSubmit={e => { e.preventDefault(); save() }} style={{ display: 'flex', flexDirection: 'column', gap: 10, opacity: enabled ? 1 : 0.6 }}>
          <div style={fieldRow}>
            <span style={label}>Coder root</span>
            <div style={{ display: 'flex', gap: 8 }}>
              <input aria-label="Coder root" value={form.workspaceRoot} onChange={set('workspaceRoot')} disabled={!!busy}
                placeholder="~/monoagent-coder" spellCheck={false} style={{ ...input, flex: 1 }} />
              <button type="button" className="btn btn-secondary btn-sm" onClick={browse} disabled={!!busy}
                style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                <FolderOpen size={12} /> Browse…
              </button>
            </div>
            <span style={hint}>Coder chats work in this folder unless you choose another.</span>
          </div>
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <div style={{ ...fieldRow, flex: '1 1 120px' }}>
              <span style={label}>Max turns</span>
              <input aria-label="Max turns" inputMode="numeric" value={form.maxTurns} onChange={set('maxTurns')} disabled={!!busy} style={input} />
            </div>
            <div style={{ ...fieldRow, flex: '1 1 120px' }}>
              <span style={label}>Timeout</span>
              <input aria-label="Timeout" value={form.timeout} onChange={set('timeout')} disabled={!!busy} placeholder="60m" style={input} />
            </div>
            {showBudget && (
              <div style={{ ...fieldRow, flex: '1 1 120px' }}>
                <span style={label}>Budget per turn (USD)</span>
                <input aria-label="Budget per turn" inputMode="decimal" value={form.budgetUsd} onChange={set('budgetUsd')} disabled={!!busy} placeholder="none" style={input} />
              </div>
            )}
          </div>
          {perRuntime && (
            <span data-testid="coder-limits-hint" style={hint}>
              The timeout applies to every runtime. Max turns applies on {limitNames('maxTurns').join(', ') || 'no ready runtime'}
              {showBudget ? `; the budget on ${limitNames('reportsCost').join(', ')}` : '; no ready runtime reports cost, so there is no budget'}.
            </span>
          )}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <button type="submit" className="btn btn-primary btn-sm" disabled={!!busy}>
              {busy === 'save' ? 'Saving…' : 'Save'}
            </button>
            {note && <span style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)' }}>{note}</span>}
          </div>
        </form>
      )}
      {err && <div role="alert" style={errText}>{err}</div>}
    </div>
  )
}
