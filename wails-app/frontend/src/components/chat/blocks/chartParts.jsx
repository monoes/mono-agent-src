import { useLayoutEffect, useState } from 'react'
import { COLOR } from './ui.js'
import { SERIES_COLORS } from './chartScale.js'

export const srOnly = { position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap' }

// useWidth: the pixel width of the element in ref (the chart is drawn at its
// real width so its text stays 10px). Falls back where ResizeObserver or
// layout is missing (tests).
export function useWidth(ref, fallback = 480) {
  const [w, setW] = useState(fallback)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return undefined
    const set = () => { const r = el.getBoundingClientRect().width; if (r > 0) setW(Math.max(160, Math.floor(r))) }
    set()
    if (typeof ResizeObserver === 'undefined') return undefined
    const ro = new ResizeObserver(set)
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref])
  return w
}

// Tooltip: HTML over the chart, placed at px (left) and flipped to the left
// of the pointer past the middle. rows: [{color?, label, value}].
export function Tooltip({ px, width, title, rows }) {
  const flip = px > width * 0.6
  return (
    <div data-testid="chart-tooltip" style={{
      position: 'absolute', top: 4, left: px, transform: `translateX(${flip ? 'calc(-100% - 8px)' : '8px'})`, pointerEvents: 'none',
      background: '#020509', border: `1px solid ${COLOR.border}`, borderRadius: 6, padding: '4px 8px', fontSize: 10, color: COLOR.text, zIndex: 2, whiteSpace: 'nowrap',
    }}>
      {title && <div style={{ color: COLOR.muted, marginBottom: 2 }}>{title}</div>}
      {rows.map((r, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
          {r.color && <span aria-hidden style={{ width: 8, height: 8, borderRadius: 2, background: r.color, flexShrink: 0 }} />}
          <span>{r.label}</span><span style={{ marginLeft: 'auto', paddingLeft: 10, fontVariantNumeric: 'tabular-nums' }}>{r.value}</span>
        </div>
      ))}
    </div>
  )
}

// Legend: always present for two or more series; identity is the swatch
// beside the name, text stays in the text color.
export function Legend({ items }) {
  return (
    <ul style={{ display: 'flex', flexWrap: 'wrap', gap: '2px 12px', listStyle: 'none', margin: '6px 0 0', padding: 0, fontSize: 10, color: COLOR.muted }}>
      {items.map((it, i) => (
        <li key={i} style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span aria-hidden style={{ width: 8, height: 8, borderRadius: 2, background: SERIES_COLORS[i] }} />{it}
        </li>
      ))}
    </ul>
  )
}
