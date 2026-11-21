// Styles and small helpers shared by the Jev section's files.

export const mono = 'var(--font-mono)'
export const card = {
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-lg)',
  padding: '16px 20px',
  display: 'flex', flexDirection: 'column', gap: 14,
  marginBottom: 16,
}
export const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
export const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
export const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
export const okText = { fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)', lineHeight: 1.5, wordBreak: 'break-word' }
export const input = {
  background: 'var(--elevated)', border: '1px solid var(--border)', color: 'var(--text)',
  borderRadius: 'var(--radius)', fontFamily: mono, fontSize: 12, padding: '6px 10px', minWidth: 0,
}

export const errMsg = (e) => String(e?.message || e || 'unknown error')

// testError shortens "jev: HTTP 401: {json body}" to "HTTP 401 — <message>".
export function testError(raw) {
  const m = /HTTP (\d+):\s*(\{.*\})\s*$/s.exec(raw || '')
  if (!m) return raw || 'unknown error'
  try {
    const body = JSON.parse(m[2])
    const d = body.detail ?? body.error ?? body
    const msg = typeof d === 'string' ? d : (d?.message || body.message)
    if (msg) return `HTTP ${m[1]} — ${msg}`
  } catch { /* keep the raw text */ }
  return raw
}

export function keyChip(st, t) {
  switch (st?.key_source) {
    case 'profile-vault': return { text: t('settings.jev.keyProfileVault'), color: 'var(--cyan)', bg: 'rgba(0,180,216,.1)', bd: 'rgba(0,180,216,.25)' }
    case 'vault': return { text: `Vault entry "${st.key_entry || 'typesafe'}"`, color: 'var(--green-neon)', bg: 'rgba(16,185,129,.1)', bd: 'rgba(74,222,128,.25)' }
    case 'env': return { text: 'Environment variable (TYPESAFE_API_KEY)', color: 'var(--cyan)', bg: 'rgba(0,180,216,.1)', bd: 'rgba(0,180,216,.25)' }
    case 'config': return { text: 'Set in configuration', color: 'var(--cyan)', bg: 'rgba(0,180,216,.1)', bd: 'rgba(0,180,216,.25)' }
    default: return { text: 'Not set', color: 'var(--yellow)', bg: 'rgba(234,179,8,.08)', bd: 'rgba(234,179,8,.22)' }
  }
}

// A small rounded badge, the style of the key chip.
export const pill = (color, bg, bd) => ({
  display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10,
  color, background: bg, border: `1px solid ${bd}`, borderRadius: 99, padding: '1px 8px', whiteSpace: 'nowrap',
})

// What a feature sends, as the CLI reports it.
export function EgressList({ items }) {
  if (!items?.length) return <div style={hint}>No details reported by the CLI.</div>
  return (
    <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 2 }}>
      {items.map((e, i) => (
        <li key={i} style={{ fontFamily: 'var(--font-body)', fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.5 }}>{e}</li>
      ))}
    </ul>
  )
}

// hostOf is a provider URL's host:port ('' when it does not parse).
export function hostOf(baseURL) {
  try { return new URL(baseURL).host } catch { return '' }
}

// isThisComputer copies the CLI's loopback rule (jevconf checkOverride): the
// host is exactly "localhost", a dotted IPv4 127.x.y.z or [::1]. Anything
// else counts as another host, so the GUI asks before sending data there.
export function isThisComputer(baseURL) {
  const m = /^https?:\/\/([^/?#@]+?)(?::\d+)?(?:[/?#]|$)/i.exec(baseURL || '')
  if (!m) return false
  const h = m[1].toLowerCase()
  if (h === 'localhost' || h === '[::1]') return true
  const ip = /^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(h)
  return !!ip && ip.slice(1).every(o => Number(o) <= 255)
}

export function formatUSD(v) {
  if (!v) return '$0'
  if (v < 0.0001) return '<$0.0001'
  return `$${v.toFixed(v < 1 ? 4 : 2)}`
}

export function Switch({ on, disabled, label: aria, onChange }) {
  return (
    <button
      type="button" role="switch" aria-checked={on} aria-label={aria} disabled={disabled}
      onClick={() => onChange(!on)}
      style={{
        width: 32, height: 18, borderRadius: 9, flexShrink: 0, position: 'relative', padding: 0,
        border: `1px solid ${on ? 'var(--cyan)' : 'var(--border-bright)'}`,
        background: on ? 'rgba(0,180,216,.35)' : 'var(--elevated)',
        cursor: disabled ? 'not-allowed' : 'pointer', opacity: disabled ? 0.5 : 1,
        transition: 'all var(--transition)',
      }}
    >
      <span style={{
        position: 'absolute', top: 2, left: on ? 16 : 2, width: 12, height: 12, borderRadius: '50%',
        background: on ? 'var(--cyan-bright)' : 'var(--text-dim)', transition: 'left var(--transition)',
      }} />
    </button>
  )
}
