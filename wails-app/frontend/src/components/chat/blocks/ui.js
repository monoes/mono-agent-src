// Shared look of chat blocks: the dark chat surface tokens already used by
// ChatMarkdown / the message bubble.
export const COLOR = {
  surface: '#0d1a28',
  border: '#1e3a4f',
  text: '#e2e8f0',
  muted: '#94a3b8',
  accent: '#00b4d8',
  ok: '#34d399',
  warn: '#fbbf24',
  error: '#f87171',
}

export const panel = {
  border: `1px solid ${COLOR.border}`, borderRadius: 8, background: COLOR.surface,
  padding: '8px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: COLOR.text,
}

export const heading = { fontSize: 11, fontWeight: 600, color: COLOR.text, margin: '0 0 6px' }

export const note = { fontSize: 10, color: COLOR.muted, marginTop: 6 }

export const optionButton = (state) => ({
  display: 'flex', alignItems: 'center', gap: 8, width: '100%', textAlign: 'left', cursor: state === 'idle' ? 'pointer' : 'default',
  padding: '6px 8px', borderRadius: 6, fontFamily: 'var(--font-mono)', fontSize: 11, color: COLOR.text,
  border: `1px solid ${state === 'chosen' ? COLOR.accent : COLOR.border}`,
  background: state === 'chosen' ? 'rgba(0,180,216,0.12)' : 'transparent',
  opacity: state === 'dim' ? 0.5 : 1,
})

export const primaryButton = (disabled) => ({
  marginTop: 8, padding: '4px 12px', borderRadius: 6, border: `1px solid ${COLOR.accent}`,
  background: 'rgba(0,180,216,0.15)', color: COLOR.accent, fontFamily: 'var(--font-mono)', fontSize: 11,
  cursor: disabled ? 'default' : 'pointer', opacity: disabled ? 0.5 : 1,
})
