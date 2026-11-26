import { useTranslation } from 'react-i18next'
import t from '../../lib/i18nOr.js'
import { ExternalLink } from 'lucide-react'
import { api, notify } from '../../services/api.js'

export function formatDate(s) {
  if (!s) return ''
  const d = new Date(s.replace(' ', 'T'))
  return isNaN(d) ? s : d.toLocaleString()
}

export function isImageValue(value) {
  if (typeof value !== 'string') return false
  return /\.(png|jpe?g|gif|webp|svg|bmp)(\?.*)?$/i.test(value) ||
    value.startsWith('data:image/') ||
    value.startsWith('blob:')
}

export const PLATFORM_PROFILE_URL = {
  INSTAGRAM: (u) => `https://www.instagram.com/${u}/`,
  LINKEDIN:  (u) => `https://www.linkedin.com/in/${u}/`,
  X:         (u) => `https://x.com/${u}`,
  TIKTOK:    (u) => `https://www.tiktok.com/@${u}`,
}

export function ReadonlyField({ label, value }) {
  const display = typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value ?? '')
  const isImg = isImageValue(display)
  const isUrl = typeof display === 'string' && /^https?:\/\//i.test(display)
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 3 }}>
        {label}
      </div>
      {isImg ? (
        <div style={{ borderRadius: 6, overflow: 'hidden', border: '1px solid rgba(255,255,255,0.07)', background: 'rgba(255,255,255,0.04)' }}>
          <img src={display} alt={label} style={{ maxWidth: '100%', maxHeight: 200, display: 'block', objectFit: 'contain' }} />
          <div style={{ padding: '4px 8px', fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', wordBreak: 'break-all' }}>{display}</div>
        </div>
      ) : (
        <div style={{
          padding: '7px 10px',
          background: 'rgba(255,255,255,0.04)',
          borderRadius: 6,
          border: '1px solid rgba(255,255,255,0.07)',
          fontSize: 13,
          color: 'var(--text-secondary)',
          wordBreak: 'break-all',
        }}>
          {isUrl ? (
            <button
              type="button"
              onClick={() => api.openURL(display)}
              style={{
                background: 'none', border: 'none', padding: 0,
                color: '#00b4d8', cursor: 'pointer', textAlign: 'left',
                wordBreak: 'break-all', fontSize: 13, textDecoration: 'underline',
                display: 'inline-flex', alignItems: 'center', gap: 4,
              }}
            >
              {display} <ExternalLink size={11} style={{ flexShrink: 0 }} />
            </button>
          ) : display}
        </div>
      )}
    </div>
  )
}

export function EditableField({ label, value, onChange }) {
  useTranslation() // renders again on a language change
  const isLong = typeof value === 'string' && value.length > 80
  return (
    <div style={{ marginBottom: 12 }}>
      <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: '#00b4d8', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 4 }}>
        {label} <span style={{ color: 'var(--text-muted)', fontWeight: 400 }}>{t('ui7Hil.editable')}</span>
      </div>
      <textarea
        value={typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value ?? '')}
        onChange={e => onChange(e.target.value)}
        rows={isLong ? 6 : 3}
        style={{
          width: '100%',
          boxSizing: 'border-box',
          padding: '9px 12px',
          background: 'rgba(0,180,216,0.04)',
          border: '1px solid rgba(0,180,216,0.25)',
          borderRadius: 6,
          fontSize: 13,
          color: 'var(--text-primary)',
          fontFamily: 'inherit',
          resize: 'vertical',
          outline: 'none',
          lineHeight: 1.6,
        }}
        onFocus={e => { e.target.style.borderColor = 'rgba(0,180,216,0.6)' }}
        onBlur={e => { e.target.style.borderColor = 'rgba(0,180,216,0.25)' }}
      />
    </div>
  )
}
