import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { JevSetKey, JevTestKey, JevRemoveKey } from '../../wailsjs/go/main/App'
import { mono, label, hint, errText, okText, input, keyChip, testError } from './jevShared.jsx'
import i18nOr from '../../lib/i18nOr.js'

// The key's state lives in the section (not in the row), so folding the section
// does not lose a half-typed key. `run` is the section's one-call-at-a-time
// wrapper; `reload` reads the shared status again.
export function useJevKey(run, reload) {
  const [keyInput, setKeyInput] = useState('')
  const [keyNote, setKeyNote] = useState(null) // { key, entry }: translated when shown
  const [test, setTest] = useState(null)
  const [confirmRemove, setConfirmRemove] = useState(false)

  const saveKey = () => run('save', async () => {
    const r = await JevSetKey(keyInput)
    setKeyInput(''); setTest(null); setConfirmRemove(false)
    setKeyNote({ key: r?.replaced ? 'ui6SetJev.note_replaced' : 'ui6SetJev.note_saved', entry: r?.key_entry || 'typesafe' })
    await reload()
  })
  const testKey = () => run('test', async () => { setTest(null); setTest(await JevTestKey()) })
  const removeKey = () => run('remove', async () => {
    const r = await JevRemoveKey()
    setConfirmRemove(false); setTest(null)
    setKeyNote({ key: 'ui6SetJev.note_removed', entry: r?.removed || 'typesafe' })
    await reload()
  })

  return { keyInput, setKeyInput, keyNote, test, confirmRemove, setConfirmRemove, saveKey, testKey, removeKey }
}

// API key: the chip for where the key comes from, the paste/save/test/remove
// form, the profile-vault "adopt" hint and the result of the last test.
export default function JevKeyRow({ status, busy, k }) {
  const { t } = useTranslation()
  const chip = keyChip(status, t)
  const hasKey = status.key_source && status.key_source !== 'none'
  const disabled = !!busy
  const removable = status.key_source === 'vault'
  const { keyInput, setKeyInput, keyNote, test, confirmRemove, setConfirmRemove, saveKey, testKey, removeKey } = k

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, borderTop: '1px solid var(--border-dim)', paddingTop: 12 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span style={label}>{t('settings.jev.providers.typesafeKey')}</span>
        <span data-testid="jev-key-chip" style={{
          display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10.5,
          color: chip.color, background: chip.bg, border: `1px solid ${chip.bd}`, borderRadius: 99, padding: '2px 9px',
        }}>
          <KeyRound size={11} /> {chip.text}
        </span>
        {status.model && <span style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-dim)' }}>{t('ui6SetJev.model', { model: status.model })}</span>}
      </div>
      <form
        onSubmit={e => { e.preventDefault(); if (keyInput.trim()) saveKey() }}
        style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}
      >
        <input
          type="password" autoComplete="off" spellCheck={false} aria-label={t('ui6SetJev.key_label')}
          placeholder={t(hasKey ? 'ui6SetJev.key_replace' : 'ui6SetJev.key_paste')}
          value={keyInput} onChange={e => setKeyInput(e.target.value)} disabled={disabled}
          style={{ ...input, flex: '1 1 220px' }}
        />
        <button type="submit" className="btn btn-primary btn-sm" disabled={disabled || !keyInput.trim()}>
          {busy === 'save' ? t('ui6SetCoder.saving') : t('ui6SetCoder.save')}
        </button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={disabled || !hasKey} onClick={testKey}>
          {busy === 'test' ? t('ui6SetJev.testing') : t('ui6SetJev.test')}
        </button>
        {removable && !confirmRemove && (
          <button type="button" className="btn btn-danger btn-sm" disabled={disabled} onClick={() => setConfirmRemove(true)}>{i18nOr('ui6SetJev.remove')}</button>
        )}
      </form>
      {status.key_source === 'profile-vault' && (
        <div data-testid="jev-adopt-hint" style={hint}>
          {t('settings.jev.adoptHint')} <code style={{ fontFamily: mono }}>monoagentcli jev key adopt</code>
        </div>
      )}
      {status.key_source === 'env' && (
        <div style={hint}>{t('ui6SetJev.env_hint')}</div>
      )}
      {confirmRemove && (
        <div data-testid="jev-remove-confirm" style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span style={{ fontFamily: mono, fontSize: 11, color: 'var(--red)', flex: 1, minWidth: 180 }}>
            {t('ui6SetJev.remove_confirm', { entry: status.key_entry || 'typesafe' })}
          </span>
          <button className="btn btn-secondary btn-sm" disabled={disabled} onClick={() => setConfirmRemove(false)}>{t('ui6Set.cancel')}</button>
          <button className="btn btn-danger btn-sm" disabled={disabled} onClick={removeKey}>
            {busy === 'remove' ? t('ui6SetJev.deleting') : i18nOr('ui6SetJev.delete_key')}
          </button>
        </div>
      )}
      {keyNote && <div style={okText}>{t(keyNote.key, { entry: keyNote.entry })}</div>}
      {test && (test.ok ? (
        <div data-testid="jev-test-result" style={okText}>
          {t('ui6SetJev.key_works', { models: test.models?.length ? ` — ${t('ui6SetJev.models', { list: test.models.join(', ') })}` : '' })}
        </div>
      ) : (
        <div data-testid="jev-test-result" style={errText}>{t('ui6SetJev.key_failed', { error: testError(test.error) })}</div>
      ))}
    </div>
  )
}
