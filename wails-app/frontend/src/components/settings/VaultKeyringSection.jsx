import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { LockKeyhole } from 'lucide-react'
import { KeyringStatus, KeyringSetPassphrase, KeyringClearPassphrase } from '../../wailsjs/go/main/App'
import i18nOr from '../../lib/i18nOr.js'

// Vault keyring passphrase, for hosts with no OS keychain. Shown only when
// `monoagentcli secret keyring status` reports backend "file": there the
// vault key lives in a passphrase-protected file keyring, and the desktop
// app has no terminal to ask for that passphrase — so every vault write
// (e.g. saving the Jev key) fails until it is saved here. Everything goes
// through the CLI (Go side: app_keyring.go); the passphrase is sent once
// on stdin and never read back.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-lg)',
  padding: '16px 20px',
  display: 'flex', flexDirection: 'column', gap: 12,
  marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const okText = { fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)', lineHeight: 1.5, wordBreak: 'break-word' }
const input = {
  background: 'var(--elevated)', border: '1px solid var(--border)', color: 'var(--text)',
  borderRadius: 'var(--radius)', fontFamily: mono, fontSize: 12, padding: '6px 10px', minWidth: 0,
}
const code = { fontFamily: mono, fontSize: 10.5, color: 'var(--text-secondary)' }

const errMsg = (e) => String(e?.message || e || i18nOr('ui6SetCoder.unknown_error'))

// passChip describes where the file keyring's passphrase comes from.
export function passChip(pf) {
  if (!pf?.configured) {
    return { text: i18nOr('ui6SetVault.not_saved'), color: 'var(--yellow)', bg: 'rgba(234,179,8,.08)', bd: 'rgba(234,179,8,.22)' }
  }
  if (!pf.ok) {
    return { text: i18nOr('ui6SetVault.file_problem'), color: 'var(--red)', bg: 'rgba(239,68,68,.08)', bd: 'rgba(239,68,68,.25)' }
  }
  if (pf.source === 'env') {
    return { text: i18nOr('ui6SetVault.from_env'), color: 'var(--cyan)', bg: 'rgba(0,180,216,.1)', bd: 'rgba(0,180,216,.25)' }
  }
  return { text: i18nOr('ui6SetVault.saved'), color: 'var(--green-neon)', bg: 'rgba(16,185,129,.1)', bd: 'rgba(74,222,128,.25)' }
}

export default function VaultKeyringSection() {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [pass, setPass] = useState('')
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [note, setNote] = useState(null) // { key, path }: translated when shown

  const load = useCallback(async () => {
    try { setStatus(await KeyringStatus()) } catch { setStatus(null) }
  }, [])
  useEffect(() => { load() }, [load])

  // Hosts with an OS keychain (and CLIs too old to answer) never see this.
  if (status?.backend !== 'file') return null

  const run = async (what, fn) => {
    setBusy(what); setErr(''); setNote(null)
    try { await fn() } catch (e) { setErr(errMsg(e)) } finally { setBusy('') }
  }
  const save = () => run('save', async () => {
    const r = await KeyringSetPassphrase(pass)
    setPass('')
    setNote({ key: 'ui6SetVault.saved_to', path: r?.path || '~/.monoagent/keyring-passphrase' })
    await load()
  })
  const forget = () => run('forget', async () => {
    const r = await KeyringClearPassphrase()
    setNote({ key: r?.removed ? 'ui6SetVault.removed' : 'ui6SetVault.nothing_to_remove' })
    await load()
  })

  const pf = status.passphrase_file || {}
  const chip = passChip(pf)
  const hasKeyring = (status.file_keyrings || []).length > 0
  const disabled = !!busy

  return (
    <div id="settings-vault-keyring" data-testid="vault-keyring-section">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
        <span style={{ fontFamily: mono, fontSize: 10, fontWeight: 700, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: 2 }}>
          {t('ui6SetVault.title')}
        </span>
        <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
      </div>
      <div style={card}>
        <div style={hint}>
          {t('ui6SetVault.about')}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <span style={label}>{t('ui6SetVault.passphrase')}</span>
          <span data-testid="vault-keyring-chip" style={{
            display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10.5,
            color: chip.color, background: chip.bg, border: `1px solid ${chip.bd}`, borderRadius: 99, padding: '2px 9px',
          }}>
            <LockKeyhole size={11} /> {chip.text}
          </span>
          {pf.configured && pf.path && (
            <span style={code}>{pf.path}{pf.mode ? ` · ${t('ui6SetVault.mode', { mode: pf.mode })}` : ''}</span>
          )}
        </div>
        {pf.configured && !pf.ok && pf.error && <div style={errText}>{pf.error}</div>}
        {pf.source === 'env' && (
          <div style={hint}>
            {t('ui6SetVault.env_hint')}
          </div>
        )}

        <form
          onSubmit={e => { e.preventDefault(); if (pass) save() }}
          style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}
        >
          <input
            type="password" autoComplete="off" spellCheck={false} aria-label={t('ui6SetVault.input_label')}
            placeholder={t(hasKeyring ? 'ui6SetVault.existing_placeholder' : 'ui6SetVault.new_placeholder')}
            value={pass} onChange={e => setPass(e.target.value)} disabled={disabled}
            style={{ ...input, flex: '1 1 240px' }}
          />
          <button type="submit" className="btn btn-primary btn-sm" disabled={disabled || !pass}>
            {busy === 'save' ? t('ui6SetVault.checking') : t('ui6SetCoder.save')}
          </button>
          {pf.source === 'configured' && (
            <button type="button" className="btn btn-secondary btn-sm" disabled={disabled} onClick={forget}>
              {busy === 'forget' ? t('ui6SetVault.removing') : t('ui6SetVault.forget')}
            </button>
          )}
        </form>
        <div style={hint}>
          {hasKeyring
            ? t('ui6SetVault.exists')
            : t('ui6SetVault.none_yet')}
        </div>
        {note && <div style={okText}>{t(note.key, { path: note.path })}</div>}
        {err && <div role="alert" style={errText}>{err}</div>}
      </div>
    </div>
  )
}
