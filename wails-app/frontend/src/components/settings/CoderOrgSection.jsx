import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { Users, Send, AppWindow, X } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'

// Coder chats' dynamic org and agents' sends (epic #475): settings the CLI
// has had without a GUI. part="machine" is this computer's (`coder set
// --org-router`, `--send-approval`, `--app-access`); part="profile" is the
// profile's always-allow list (`hil allow list|add|remove`). The app runs
// the CLI as the user, so the operator-only setters work from here.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const row = { display: 'flex', alignItems: 'center', gap: 12, borderTop: '1px solid var(--border)', paddingTop: 10 }
const name = { fontFamily: 'var(--font-body)', fontSize: 12.5, color: 'var(--text)' }

// The routers `coder set --org-router` takes, and the actions `hil allow
// add --action` takes.
export const ORG_ROUTERS = ['auto', 'jev', 'model']
export const SEND_ACTIONS = ['email', 'dm', 'comment', 'post', 'connect', 'publish']

const errMsg = (e) => String(e?.message || e || 'unknown error')

// labelLike says whether a recipient reads like a label ("the ticket's
// requester") rather than an address, handle or name: once invisible format
// characters are dropped, it starts with "the " and holds no @ or digit. A
// rule for it would match no send, so the form refuses it (#871 review). A
// courtesy: `hil allow add` makes the same check (hilsend.LabelLike).
export function labelLike(recipient) {
  const v = String(recipient ?? '').replace(/\p{Cf}/gu, '').trim()
  return /^the\s/i.test(v) && !/[@\d]/.test(v)
}

function MachinePart() {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    try { setStatus(await api.coderStatus()); setLoadErr('') } catch (e) { setLoadErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  const run = async (what, fn) => {
    setBusy(what); setErr('')
    try { setStatus(await fn()) } catch (e) { setErr(errMsg(e)) } finally { setBusy('') }
  }

  const setRouter = (orgRouter) => run('router', () => api.coderSet({ orgRouter }))
  const setAppAccess = async (on) => {
    if (on) {
      const ok = await confirm(t('coderOrg.appAccessOnBody'), {
        title: t('coderOrg.appAccessOnTitle'), confirmLabel: t('coderOrg.appAccessOnConfirm'), danger: true,
      })
      if (!ok) return
    }
    await run('apps', () => api.coderSetAppAccess(on))
  }
  const setSendApproval = async (on) => {
    if (!on) {
      const ok = await confirm(t('coderOrg.sendApprovalOffBody'), {
        title: t('coderOrg.sendApprovalOffTitle'), confirmLabel: t('coderOrg.sendApprovalOffConfirm'), danger: true,
      })
      if (!ok) return
    }
    await run('send', () => api.hilSendSetApproval(on))
  }

  if (!status) {
    return (
      <div style={card} data-testid="coder-org-section">
        {loadErr ? <div role="alert" style={errText}>{t('coderOrg.unavailable', { error: loadErr })}</div> : <div style={hint}>{t('coderOrg.allowLoading')}</div>}
      </div>
    )
  }
  const router = ORG_ROUTERS.includes(status.orgRouter) ? status.orgRouter : 'auto'
  const sendApproval = status.sendApproval !== false
  return (
    <div style={card} data-testid="coder-org-section">
      <div style={hint}>{t('coderOrg.machineHint')}</div>
      {err && <div role="alert" style={errText}>{err}</div>}

      <div style={{ ...row, alignItems: 'flex-start' }}>
        <Users size={13} color="#00b4d8" style={{ flexShrink: 0, marginTop: 2 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={name}>{t('coderOrg.routerLabel')}</div>
          <div style={hint}>{t('coderOrg.routerHint')} {t(`coderOrg.routerDesc.${router}`)}</div>
          <div style={hint}>{t('coderOrg.routerJevNote')}</div>
        </div>
        <select aria-label={t('coderOrg.routerLabel')} value={router} disabled={!!busy}
          onChange={e => setRouter(e.target.value)} style={{ minWidth: 170 }}>
          {ORG_ROUTERS.map(r => <option key={r} value={r} title={t(`coderOrg.routerDesc.${r}`)}>{t(`coderOrg.router.${r}`)}</option>)}
        </select>
      </div>

      <label style={{ ...row, alignItems: 'flex-start', cursor: busy ? 'default' : 'pointer' }}>
        <Send size={13} color="#00b4d8" style={{ flexShrink: 0, marginTop: 2 }} />
        <span style={{ flex: 1, minWidth: 0 }}>
          <span style={name}>{t('coderOrg.sendApproval')}</span>
          {busy === 'send' && <span style={{ ...hint, marginLeft: 6 }}>{t('coderOrg.saving')}</span>}
          <span style={{ ...hint, display: 'block' }}>{t('coderOrg.sendApprovalHint')}</span>
        </span>
        <input type="checkbox" checked={sendApproval} disabled={!!busy} aria-label={t('coderOrg.sendApproval')}
          onChange={e => setSendApproval(e.target.checked)} style={{ marginTop: 2, accentColor: '#00b4d8', flexShrink: 0 }} />
      </label>

      {/* A monoagentcli without the kill switch reports no appAccess. */}
      {typeof status.appAccess === 'boolean' && (
        <label style={{ ...row, alignItems: 'flex-start', cursor: busy ? 'default' : 'pointer' }}>
          <AppWindow size={13} color="#00b4d8" style={{ flexShrink: 0, marginTop: 2 }} />
          <span style={{ flex: 1, minWidth: 0 }}>
            <span style={name}>{t('coderOrg.appAccess')}</span>
            {busy === 'apps' && <span style={{ ...hint, marginLeft: 6 }}>{t('coderOrg.saving')}</span>}
            <span style={{ ...hint, display: 'block' }}>{t('coderOrg.appAccessHint')}</span>
          </span>
          <input type="checkbox" checked={status.appAccess} disabled={!!busy} aria-label={t('coderOrg.appAccess')}
            onChange={e => setAppAccess(e.target.checked)} style={{ marginTop: 2, accentColor: '#00b4d8', flexShrink: 0 }} />
        </label>
      )}
    </div>
  )
}

function ProfilePart() {
  const { t } = useTranslation()
  const [rules, setRules] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [form, setForm] = useState({ action: 'email', platform: '', account: '', recipient: '' })

  const load = useCallback(async () => {
    try {
      const list = await api.hilSendAllowList()
      setRules(Array.isArray(list) ? list : [])
      setLoadErr('')
    } catch (e) { setLoadErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  // run makes one change through the CLI, then reads the list back.
  const run = async (what, fn) => {
    setBusy(what); setErr('')
    try { await fn(); await load(); return true } catch (e) { setErr(errMsg(e)); return false } finally { setBusy('') }
  }
  const add = async (e) => {
    e.preventDefault()
    const f = { action: form.action, platform: form.platform.trim(), account: form.account.trim(), recipient: form.recipient.trim() }
    if (!f.recipient || labelLike(f.recipient)) return
    if (await run('add', () => api.hilSendAllowAdd(f.action, f.platform, f.account, f.recipient))) {
      setForm(v => ({ ...v, recipient: '' }))
    }
  }
  const set = (k) => (e) => setForm(v => ({ ...v, [k]: e.target.value }))
  const notAddress = labelLike(form.recipient)

  return (
    <div style={card} data-testid="coder-allow-section">
      <div style={label}>{t('coderOrg.allowTitle')}</div>
      <div style={hint}>{t('coderOrg.allowHint')}</div>
      {loadErr && <div role="alert" style={errText}>{t('coderOrg.unavailable', { error: loadErr })}</div>}
      {err && <div role="alert" style={errText}>{err}</div>}
      {!rules && !loadErr && <div style={hint}>{t('coderOrg.allowLoading')}</div>}
      {rules && rules.length === 0 && <div style={hint}>{t('coderOrg.allowEmpty')}</div>}
      {(rules || []).map(r => (
        <div key={r.id} data-rule={r.id} style={row}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ ...name, fontFamily: mono, fontSize: 11.5, wordBreak: 'break-all' }}>{r.recipient}</div>
            <div style={hint}>
              {t(`coderOrg.action.${r.action}`, { defaultValue: r.action })} · {r.platform || '—'} · {r.account || '—'}
            </div>
          </div>
          <button type="button" className="btn btn-ghost btn-sm" disabled={!!busy}
            aria-label={t('coderOrg.allowRemove', { recipient: r.recipient })} title={t('coderOrg.allowRemove', { recipient: r.recipient })}
            onClick={() => run('remove', () => api.hilSendAllowRemove(r.id))}>
            <X size={12} />
          </button>
        </div>
      ))}
      {rules && (
        <form onSubmit={add} style={{ ...row, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <select aria-label={t('coderOrg.allowAction')} value={form.action} onChange={set('action')} disabled={!!busy} style={{ minWidth: 140 }}>
            {SEND_ACTIONS.map(a => <option key={a} value={a}>{t(`coderOrg.action.${a}`)}</option>)}
          </select>
          <input className="form-input" aria-label={t('coderOrg.allowPlatform')} placeholder={t('coderOrg.allowPlatform')}
            value={form.platform} onChange={set('platform')} disabled={!!busy} spellCheck={false} style={{ width: 120, fontSize: 12 }} />
          <input className="form-input" aria-label={t('coderOrg.allowAccount')} placeholder={t('coderOrg.allowAccount')}
            value={form.account} onChange={set('account')} disabled={!!busy} spellCheck={false} style={{ width: 160, fontSize: 12 }} />
          <input className="form-input" aria-label={t('coderOrg.allowRecipient')} placeholder={t('coderOrg.allowRecipientPlaceholder')}
            value={form.recipient} onChange={set('recipient')} disabled={!!busy} spellCheck={false} style={{ flex: 1, minWidth: 200, fontSize: 12 }}
            aria-invalid={notAddress || undefined} aria-describedby={notAddress ? 'coder-allow-recipient-label' : undefined} />
          <button type="submit" className="btn btn-primary btn-sm" disabled={!!busy || !form.recipient.trim() || notAddress}>{t('coderOrg.allowAdd')}</button>
          {notAddress && <div id="coder-allow-recipient-label" style={{ ...errText, flexBasis: '100%' }}>{t('coderOrg.allowRecipientLabel')}</div>}
        </form>
      )}
    </div>
  )
}

export default function CoderOrgSection({ part = 'machine' }) {
  return part === 'profile' ? <ProfilePart /> : <MachinePart />
}
