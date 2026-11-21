import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { JevSetKey, JevTestKey, JevRemoveKey } from '../../wailsjs/go/main/App'
import { mono, label, hint, errText, okText, input, keyChip, testError } from './jevShared.jsx'

// The key's state lives in the section (not in the row), so folding the section
// does not lose a half-typed key. `run` is the section's one-call-at-a-time
// wrapper; `reload` reads the shared status again.
export function useJevKey(run, reload) {
  const [keyInput, setKeyInput] = useState('')
  const [keyNote, setKeyNote] = useState('')
  const [test, setTest] = useState(null)
  const [confirmRemove, setConfirmRemove] = useState(false)

  const saveKey = () => run('save', async () => {
    const r = await JevSetKey(keyInput)
    setKeyInput(''); setTest(null); setConfirmRemove(false)
    setKeyNote(`${r?.replaced ? 'Replaced' : 'Saved to'} vault entry "${r?.key_entry || 'typesafe'}".`)
    await reload()
  })
  const testKey = () => run('test', async () => { setTest(null); setTest(await JevTestKey()) })
  const removeKey = () => run('remove', async () => {
    const r = await JevRemoveKey()
    setConfirmRemove(false); setTest(null)
    setKeyNote(`Removed vault entry "${r?.removed || 'typesafe'}".`)
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
        {status.model && <span style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-dim)' }}>model {status.model}</span>}
      </div>
      <form
        onSubmit={e => { e.preventDefault(); if (keyInput.trim()) saveKey() }}
        style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}
      >
        <input
          type="password" autoComplete="off" spellCheck={false} aria-label="TypeSafe API key"
          placeholder={hasKey ? 'Paste a new key to replace it' : 'Paste your TypeSafe API key'}
          value={keyInput} onChange={e => setKeyInput(e.target.value)} disabled={disabled}
          style={{ ...input, flex: '1 1 220px' }}
        />
        <button type="submit" className="btn btn-primary btn-sm" disabled={disabled || !keyInput.trim()}>
          {busy === 'save' ? 'Saving…' : 'Save'}
        </button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={disabled || !hasKey} onClick={testKey}>
          {busy === 'test' ? 'Testing…' : 'Test'}
        </button>
        {removable && !confirmRemove && (
          <button type="button" className="btn btn-danger btn-sm" disabled={disabled} onClick={() => setConfirmRemove(true)}>Remove</button>
        )}
      </form>
      {status.key_source === 'profile-vault' && (
        <div data-testid="jev-adopt-hint" style={hint}>
          {t('settings.jev.adoptHint')} <code style={{ fontFamily: mono }}>monoagentcli jev key adopt</code>
        </div>
      )}
      {status.key_source === 'env' && (
        <div style={hint}>The key comes from TYPESAFE_API_KEY. A key saved here goes to the vault and is used instead.</div>
      )}
      {confirmRemove && (
        <div data-testid="jev-remove-confirm" style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span style={{ fontFamily: mono, fontSize: 11, color: 'var(--red)', flex: 1, minWidth: 180 }}>
            Delete vault entry "{status.key_entry || 'typesafe'}"? Jev features stop working until a new key is added.
          </span>
          <button className="btn btn-secondary btn-sm" disabled={disabled} onClick={() => setConfirmRemove(false)}>Cancel</button>
          <button className="btn btn-danger btn-sm" disabled={disabled} onClick={removeKey}>
            {busy === 'remove' ? 'Deleting…' : 'Delete key'}
          </button>
        </div>
      )}
      {keyNote && <div style={okText}>{keyNote}</div>}
      {test && (test.ok ? (
        <div data-testid="jev-test-result" style={okText}>
          Key works{test.models?.length ? ` — models: ${test.models.join(', ')}` : ''}.
        </div>
      ) : (
        <div data-testid="jev-test-result" style={errText}>Key test failed: {testError(test.error)}</div>
      ))}
    </div>
  )
}
