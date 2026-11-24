import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Trash2 } from 'lucide-react'
import * as WailsApp from '../../wailsjs/go/main/App'
import { iconUrl } from '../orgdesigner/roleIcons.js'
import IconPickerModal from '../orgdesigner/IconPickerModal.jsx'
import { useEscapeClose } from '../../lib/escapeStack.js'

const card = {
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-lg)',
  padding: '16px 20px',
  marginBottom: 16,
}
const mono = { fontFamily: 'var(--font-mono)', fontSize: 11 }

// A click right after a step changed is ignored: Enter or a double-click must
// not carry the user through two steps.
const STEP_SETTLE_MS = 600

// A table name read as words: "person_messages" -> "person messages".
const words = (table) => table.replace(/_/g, ' ')

/**
 * DeleteProfileDialog — three steps before a profile goes: what is lost; a
 * second step with its own control (a box to tick); then typing the name.
 */
export function DeleteProfileDialog({ profile, onClose, onDeleted }) {
  const { t } = useTranslation()
  const [step, setStep] = useState(1)
  const [summary, setSummary] = useState(null)
  const [typed, setTyped] = useState('')
  const [acked, setAcked] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [backup, setBackup] = useState(null) // set once the profile is gone
  const stepAt = useRef(Date.now())
  const tables = summary?.tables ?? null

  useEffect(() => {
    WailsApp.GetProfileDeleteSummary(profile.id)
      .then(s => setSummary({ ...s, tables: s?.tables || [] }))
      .catch(e => setError(e?.message || String(e)))
  }, [profile.id])

  useEscapeClose(() => { if (!busy && backup === null) onClose() })

  const total = (tables || []).reduce((n, row) => n + row.rows, 0)

  const next = () => {
    if (Date.now() - stepAt.current < STEP_SETTLE_MS) return
    stepAt.current = Date.now()
    setStep(s => s + 1)
  }

  const remove = async () => {
    if (Date.now() - stepAt.current < STEP_SETTLE_MS) return
    setBusy(true)
    setError('')
    try {
      const path = await WailsApp.DeleteProfile(profile.id)
      setBackup(path || '')
    } catch (e) {
      setError(e?.message || String(e))
    } finally {
      setBusy(false)
    }
  }

  const btn = { padding: '7px 14px', borderRadius: 6, cursor: 'pointer', fontSize: 12, fontFamily: 'var(--font-mono)' }
  const danger = { ...btn, border: 'none', background: '#ef4444', color: '#fff' }

  if (backup !== null) {
    return (
      <div className="modal-overlay">
        <div role="dialog" aria-modal="true" aria-label={t('settings.profile.doneTitle')} className="modal" style={{ width: 480 }}>
          <div className="modal-title">{t('settings.profile.doneTitle')}</div>
          {backup && <div style={{ ...mono, color: 'var(--text-secondary)', lineHeight: 1.6, wordBreak: 'break-all' }}>{t('settings.profile.doneBackup', { path: backup })}</div>}
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 16 }}>
            <button onClick={onDeleted} style={{ ...btn, border: 'none', background: '#00b4d8', color: '#fff' }}>{t('settings.profile.done')}</button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget && !busy) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label={t('settings.profile.deleteButton')} className="modal" style={{ width: 480 }}>
        <div className="modal-title" style={{ color: '#ef4444' }}>{t('settings.profile.dialogTitle', { name: profile.name, step })}</div>

        {step === 1 && (
          <>
            <div style={{ ...mono, color: 'var(--text-secondary)', lineHeight: 1.6, marginBottom: 10 }}>{t('settings.profile.step1Intro')}</div>
            {tables === null && !error && <div style={{ ...mono, color: 'var(--text-muted)' }}>{t('settings.profile.counting')}</div>}
            {tables && (
              <div style={{ maxHeight: 200, overflowY: 'auto', border: '1px solid var(--border-dim)', borderRadius: 6, marginBottom: 10 }}>
                {tables.length === 0 && <div style={{ ...mono, padding: '7px 10px', color: 'var(--text-muted)' }}>{t('settings.profile.noData')}</div>}
                {tables.map(row => (
                  <div key={row.table} style={{ ...mono, display: 'flex', justifyContent: 'space-between', padding: '5px 10px', borderBottom: '1px solid var(--border-dim)' }}>
                    <span>{t(`settings.profile.tables.${row.table}`, { defaultValue: words(row.table) })}</span><span>{row.rows}</span>
                  </div>
                ))}
              </div>
            )}
            <div style={{ ...mono, color: 'var(--text-muted)', lineHeight: 1.6 }}>
              {t('settings.profile.step1Detail', { folder: summary?.folder || '' })}
            </div>
          </>
        )}

        {step === 2 && (
          <>
            <div style={{ ...mono, color: 'var(--text-secondary)', lineHeight: 1.6 }}>
              {t('settings.profile.step2Total', { count: total })}
              {' '}{t('settings.profile.step2Active')}
            </div>
            <div style={{ ...mono, color: 'var(--text-muted)', lineHeight: 1.6, marginTop: 10 }}>{t('settings.profile.step2Backup')}</div>
            <label style={{ ...mono, display: 'flex', alignItems: 'center', gap: 8, marginTop: 14, color: 'var(--text)' }}>
              <input type="checkbox" checked={acked} onChange={e => setAcked(e.target.checked)} />
              {t('settings.profile.step2Ack')}
            </label>
          </>
        )}

        {step === 3 && (
          <>
            <div style={{ ...mono, color: 'var(--text-secondary)', lineHeight: 1.6, marginBottom: 10 }}>
              {t('settings.profile.step3Intro', { name: profile.name })}
            </div>
            <input
              className="form-input"
              autoFocus
              value={typed}
              onChange={e => setTyped(e.target.value)}
              placeholder={profile.name}
              aria-label={profile.name}
            />
          </>
        )}

        {error && <div role="alert" style={{ ...mono, color: 'var(--red)', marginTop: 10 }}>{error}</div>}

        <div style={{ display: 'flex', justifyContent: step === 2 ? 'space-between' : 'flex-end', gap: 10, marginTop: 16 }}>
          {step === 2 && (
            <button onClick={next} disabled={!acked}
              style={{ ...danger, opacity: acked ? 1 : 0.5 }}>
              {t('settings.profile.step2Continue')}
            </button>
          )}
          <div style={{ display: 'flex', gap: 10 }}>
            <button onClick={onClose} disabled={busy}
              style={{ ...btn, background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-secondary)' }}>
              {t('settings.profile.cancel')}
            </button>
            {step === 1 && (
              <button onClick={next} disabled={tables === null} style={danger}>{t('settings.profile.step1Continue')}</button>
            )}
            {step === 3 && (
              <button onClick={remove} disabled={busy || typed !== profile.name}
                style={{ ...danger, opacity: typed === profile.name ? 1 : 0.5 }}>
                {busy ? t('settings.profile.deleting') : t('settings.profile.delete')}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

/** ProfileSection — rename the profile Settings shows, change its icon, or delete it. */
export default function ProfileSection({ profile, isActive = true }) {
  const { t } = useTranslation()
  const [name, setName] = useState(profile?.name || '')
  const [iconPickerOpen, setIconPickerOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [status, setStatus] = useState('')
  const [profileCount, setProfileCount] = useState(null)

  useEffect(() => { setName(profile?.name || '') }, [profile?.name])
  useEffect(() => {
    // Like Settings' own read: the count stays unknown rather than throwing a render.
    try { WailsApp.GetProfiles().then(l => setProfileCount(Array.isArray(l) ? l.length : null)).catch(() => {}) } catch { /* unknown */ }
  }, [profile?.id, isActive])

  if (!profile?.id) return null

  const save = async (nextName, nextIcon) => {
    setStatus('')
    try {
      await WailsApp.UpdateProfile(profile.id, nextName, nextIcon)
      // The sidebar's profile switcher reads the list on load.
      window.location.reload()
    } catch (e) {
      setStatus(e?.message || String(e))
    }
  }

  const dirty = name.trim() !== '' && name.trim() !== profile.name
  const onlyProfile = profileCount === 1
  return (
    <>
      <div style={card}>
        <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
          <button
            onClick={() => setIconPickerOpen(true)}
            title={t('settings.profile.changeIcon')}
            aria-label={t('settings.profile.changeIcon')}
            style={{ padding: 0, border: '2px solid rgba(0,180,216,0.3)', borderRadius: '50%', cursor: 'pointer', background: 'transparent', flexShrink: 0, width: 44, height: 44 }}
          >
            <img src={iconUrl(profile.icon || 'coder')} alt="" style={{ width: '100%', height: '100%', borderRadius: '50%', display: 'block' }} />
          </button>
          <input
            className="form-input"
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter' && dirty) save(name.trim(), profile.icon) }}
            aria-label={t('settings.profile.nameLabel')}
            maxLength={64}
            style={{ flex: 1 }}
          />
          <button className="btn btn-primary btn-sm" disabled={!dirty} onClick={() => save(name.trim(), profile.icon)}>{t('settings.profile.rename')}</button>
        </div>
        {status && <div role="alert" style={{ ...mono, color: 'var(--red)', marginTop: 8 }}>{status}</div>}

        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 16, marginTop: 16, paddingTop: 14, borderTop: '1px solid var(--border)' }}>
          <div style={{ ...mono, color: 'var(--text-muted)', lineHeight: 1.5 }}>
            {onlyProfile ? t('settings.profile.onlyProfile') : t('settings.profile.deleteHint')}
          </div>
          <button className="btn btn-secondary btn-sm" onClick={() => setDeleting(true)} disabled={onlyProfile}
            style={{ display: 'flex', alignItems: 'center', gap: 6, color: '#ef4444', flexShrink: 0 }}>
            <Trash2 size={13} /> {t('settings.profile.deleteButton')}
          </button>
        </div>
      </div>

      <IconPickerModal
        open={iconPickerOpen}
        currentIconId={profile.icon}
        onSelect={(icon) => save(profile.name, icon)}
        onClose={() => setIconPickerOpen(false)}
      />
      {deleting && (
        <DeleteProfileDialog
          profile={profile}
          onClose={() => setDeleting(false)}
          onDeleted={() => window.location.reload()}
        />
      )}
    </>
  )
}
