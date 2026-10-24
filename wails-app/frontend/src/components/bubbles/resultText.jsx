// Result text is data from an agent or from a workflow: shown as plain text
// only, every field capped, never as HTML, markdown or a link.
export const text = (v, max) => {
  const s = typeof v === 'string' ? v : v == null ? '' : String(v)
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}
export const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v)

export const chipStyle = (tone) => ({ color: tone, border: `1px solid ${tone}`, borderRadius: 999, padding: '1px 7px' })
export const lineStyle = { width: '100%', display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', minWidth: 0 }
export const wrapText = { color: 'var(--text)', overflowWrap: 'anywhere' }
export const mutedText = { color: 'var(--text-muted)', overflowWrap: 'anywhere' }

export function Chip({ tone, title, children }) {
  return <span title={title} style={chipStyle(tone)}>{children}</span>
}
