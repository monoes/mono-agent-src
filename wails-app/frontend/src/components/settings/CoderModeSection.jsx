import { useState, useEffect, useCallback } from 'react'
import { Code2, FolderOpen } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import FixMonomindButton from '../FixMonomindButton.jsx'
import { missingText, monomindNeedsFix, monomindUnusable, coderReady, coderRuntimes, runtimeReadiness } from '../chat/useCoderMode.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import CoderDefaultAccess from './CoderDefaultAccess.jsx'
import CoderPeercredBinding from './CoderPeercredBinding.jsx'
import i18nOr from '../../lib/i18nOr.js'

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

const errMsg = (e) => String(e?.message || e || i18nOr('ui6SetCoder.unknown_error'))

export const CODER_RISK_TITLE = 'Turn on Coder mode?'

// The confirm dialog's body: what the agent can do once this is on.
export function CoderRiskText() {
  const { t } = useTranslation()
  return (
    <div data-testid="coder-risk-text">
      <p style={{ margin: '0 0 8px' }}>{t('coderMode.riskIntro')}</p>
      <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <li>{t('coderMode.riskCommands')}</li>
        <li>{t('coderMode.riskSteer')}</li>
        <li>{t('coderMode.riskSetup')}</li>
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
    startupTimeout: st?.startupTimeout || '',
  }
}

export default function CoderModeSection() {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [form, setForm] = useState(formFrom(null))
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')

  const apply = (st) => { setStatus(st); setForm(formFrom(st)) }

  const load = useCallback(async () => {
    try { const st = await api.coderStatus(); apply(st); setLoadErr(''); return st } catch (e) { setLoadErr(errMsg(e)); return null }
  }, [])
  useEffect(() => { load() }, [load])

  const run = async (what, fn) => {
    setBusy(what); setErr(''); setNote('')
    try { await fn() } catch (e) { setErr(errMsg(e)) } finally { setBusy('') }
  }

  const toggle = async (on) => {
    if (on) {
      const ok = await confirm(<CoderRiskText />, { title: t('coderMode.riskTitle'), confirmLabel: t('coderMode.confirmOn'), danger: true, defaultCancel: true })
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
    if (form.maxTurns && !(maxTurns > 0)) throw new Error(t('coderMode.maxTurnsInvalid'))
    if (!(budget >= 0)) throw new Error(t('coderMode.budgetInvalid'))
    // The start-up limit is sent only when changed; cleared means the default again ('0').
    const startup = form.startupTimeout.trim()
    const startupTimeout = startup === (status.startupTimeout || '') ? '' : startup || '0'
    apply(await api.coderSet({ workspaceRoot: form.workspaceRoot.trim(), maxTurns: maxTurns || 0, timeout: form.timeout.trim(), budgetUsd: budget, startupTimeout }))
    setNote(t('coderMode.saved'))
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
          type="checkbox" checked={enabled} disabled={disabled} aria-label={t('ui6SetCoder.name')}
          onChange={e => toggle(e.target.checked)}
          style={{ marginTop: 2, accentColor: '#00b4d8', flexShrink: 0 }}
        />
        <span style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 11, color: 'var(--text-secondary)' }}>
            <Code2 size={12} color="#00b4d8" /> {t('ui6SetCoder.name')}
            {busy === 'enable' && <span style={{ color: 'var(--text-muted)' }}>{t('ui6SetCoder.turning_on')}</span>}
            {busy === 'disable' && <span style={{ color: 'var(--text-muted)' }}>{t('ui6SetCoder.turning_off')}</span>}
          </span>
          <span style={hint}>
            {t('ui6SetCoder.about')}
          </span>
        </span>
      </label>

      {loadErr && <div role="alert" style={errText}>{t('ui6SetCoder.unavailable', { error: loadErr })}</div>}
      {status && ready && (
        <div data-testid="coder-ready" style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)' }}>
          {t('ui6SetCoder.ready')}{status.monomindVersion ? ` · monomind ${status.monomindVersion}` : ''}
          {perRuntime ? ` · ${perRuntime.filter(r => r.ready).map(r => runtimeLabel(r.id)).join(', ')}` : status.runtime ? ` · ${status.runtime}` : ''}
        </div>
      )}
      {perRuntime && perRuntime.length > 0 && (
        <ul data-testid="coder-runtimes" aria-label={t('ui6SetCoder.runtimes')} style={{ margin: 0, paddingLeft: 16, fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', lineHeight: 1.6 }}>
          {perRuntime.map(r => (
            <li key={r.id} style={{ color: r.ready ? 'var(--text-secondary)' : undefined }}>
              {runtimeLabel(r.id)} · {runtimeReadiness(r)}
              {r.ready && r.toolActivity && r.toolActivity !== 'full' ? ` · ${t('ui6SetCoder.tool_calls', { activity: r.toolActivity })}` : ''}
            </li>
          ))}
        </ul>
      )}
      {status && !ready && (
        <div data-testid="coder-needs-update" style={warnText}>
          {t('ui6SetCoder.needs', { missing: missingText(status), when: t(monomindUnusable(status) ? 'ui6SetCoder.when_works' : status.missingCapabilities?.length || !perRuntime ? 'ui6SetCoder.when_updated' : 'ui6SetCoder.when_ready') })}
          {monomindNeedsFix(status) && <> <FixMonomindButton onDone={load} /></>}
        </div>
      )}

      {status && <CoderDefaultAccess status={status} onSaved={apply} />}

      {status && <CoderPeercredBinding status={status} onSaved={apply} />}

      {status && (
        <form onSubmit={e => { e.preventDefault(); save() }} style={{ display: 'flex', flexDirection: 'column', gap: 10, opacity: enabled ? 1 : 0.6 }}>
          <div style={fieldRow}>
            <span style={label}>{t('ui6SetCoder.root')}</span>
            <div style={{ display: 'flex', gap: 8 }}>
              <input aria-label={t('ui6SetCoder.root')} value={form.workspaceRoot} onChange={set('workspaceRoot')} disabled={!!busy}
                placeholder="~/monoagent-coder" spellCheck={false} style={{ ...input, flex: 1 }} />
              <button type="button" className="btn btn-secondary btn-sm" onClick={browse} disabled={!!busy}
                style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                <FolderOpen size={12} /> {t('ui6SetCoder.browse')}
              </button>
            </div>
            <span style={hint}>{t('ui6SetCoder.root_hint')}</span>
          </div>
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <div style={{ ...fieldRow, flex: '1 1 120px' }}>
              <span style={label}>{t('ui6SetCoder.max_turns')}</span>
              <input aria-label={t('ui6SetCoder.max_turns')} inputMode="numeric" value={form.maxTurns} onChange={set('maxTurns')} disabled={!!busy} style={input} />
            </div>
            <div style={{ ...fieldRow, flex: '1 1 120px' }}>
              <span style={label}>{t('ui6SetCoder.timeout')}</span>
              <input aria-label={t('ui6SetCoder.timeout')} value={form.timeout} onChange={set('timeout')} disabled={!!busy} placeholder="60m" style={input} />
            </div>
            {status.startupTimeoutEffective && (
              <div style={{ ...fieldRow, flex: '1 1 120px' }}>
                <span style={label}>{t('ui6SetCoder.startup_limit')}</span>
                <input aria-label={t('ui6SetCoder.startup_limit')} value={form.startupTimeout} onChange={set('startupTimeout')} disabled={!!busy} placeholder="2m" style={input} />
              </div>
            )}
            {showBudget && (
              <div style={{ ...fieldRow, flex: '1 1 120px' }}>
                <span style={label}>{t('ui6SetCoder.budget_usd')}</span>
                <input aria-label={t('ui6SetCoder.budget')} inputMode="decimal" value={form.budgetUsd} onChange={set('budgetUsd')} disabled={!!busy} placeholder={t('ui6SetCoder.none')} style={input} />
              </div>
            )}
          </div>
          {status.startupTimeoutEffective && (
            <span data-testid="coder-startup-hint" style={hint}>
              {t('ui6SetCoder.startup_hint')}{' '}
              {t(status.startupTimeoutSource === 'env' ? 'ui6SetCoder.startup_env' : 'ui6SetCoder.startup_effective', { value: status.startupTimeoutEffective })}
            </span>
          )}
          {perRuntime && (
            <span data-testid="coder-limits-hint" style={hint}>
              {t('ui6SetCoder.limits', {
                turns: limitNames('maxTurns').join(', ') || t('ui6SetCoder.no_runtime'),
                budget: showBudget ? t('ui6SetCoder.limits_budget', { names: limitNames('reportsCost').join(', ') }) : t('ui6SetCoder.limits_no_budget'),
              })}
            </span>
          )}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <button type="submit" className="btn btn-primary btn-sm" disabled={!!busy}>
              {busy === 'save' ? t('ui6SetCoder.saving') : t('ui6SetCoder.save')}
            </button>
            {note && <span style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)' }}>{note}</span>}
          </div>
        </form>
      )}
      {err && <div role="alert" style={errText}>{err}</div>}
    </div>
  )
}
