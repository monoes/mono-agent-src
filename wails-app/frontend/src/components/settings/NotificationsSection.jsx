import { useTranslation } from 'react-i18next'
import { useNotifySound } from '../bubbles/useNotifySound.js'

// The operator's notifySound setting: whether a chime sounds when a new item waits for the
// person (default on). The Mute buttons in the answer card and the inbox flip the same value.
export default function NotificationsSection() {
  const { t } = useTranslation()
  const [on, setOn] = useNotifySound()
  return (
    <div style={{
      background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
      padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
    }}>
      <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, cursor: 'pointer' }}>
        <input type="checkbox" checked={on} onChange={e => setOn(e.target.checked)} style={{ marginTop: 2, accentColor: 'var(--cyan)', flexShrink: 0 }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.5 }}>{t('personItem.settings.sound')}</span>
      </label>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', lineHeight: 1.6 }}>{t('personItem.settings.hint')}</div>
    </div>
  )
}
