import { useState } from 'react'
import { ArrowUp, ArrowDown, Minus } from 'lucide-react'
import { COLOR } from './ui.js'
import { SERIES_COLORS, SURFACE, arcPath, fmtFull } from './chartScale.js'
import { Tooltip, Legend } from './chartParts.jsx'
import { useTranslation } from 'react-i18next'
import i18nOr from '../../../lib/i18nOr.js'

const pct = (v, total) => `${Math.round((v / total) * 1000) / 10}%`

// donut: slices in the fixed color order, 2px surface gap between them,
// total in the middle; hover or focus a slice for its value and share.
export function DonutChart({ data, label }) {
  const { slices, unit } = data
  const [active, setActive] = useState(null)
  const total = slices.reduce((a, s) => a + s.value, 0)
  const S = 150, c = S / 2
  let a = 0
  const arcs = slices.map((s, i) => {
    const a0 = a
    a += (s.value / total) * Math.PI * 2
    return { s, i, a0, a1: a }
  }).filter(x => x.s.value > 0)
  const row = (s) => [{ label: s.label, value: `${fmtFull(s.value, unit)} (${pct(s.value, total)})` }]
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
      <div style={{ position: 'relative', width: S, height: S }}>
        <svg width={S} height={S} role="group" aria-label={label}>
          {arcs.map(({ s, i, a0, a1 }) => (
            <path key={i} tabIndex={0} aria-label={`${s.label}: ${fmtFull(s.value, unit)}, ${pct(s.value, total)}`}
              d={arcs.length === 1 ? `${arcPath(c, c, 46, 70, 0, Math.PI)}${arcPath(c, c, 46, 70, Math.PI, Math.PI * 2)}` : arcPath(c, c, 46, 70, a0, a1)}
              fill={SERIES_COLORS[i]} stroke={SURFACE} strokeWidth={2} style={{ outlineOffset: 1, cursor: 'default' }}
              onPointerEnter={() => setActive(i)} onPointerLeave={() => setActive(null)} onFocus={() => setActive(i)} onBlur={() => setActive(null)} />
          ))}
          <text x={c} y={c + 4} textAnchor="middle" fontSize={13} fill={COLOR.text} fontFamily="var(--font-mono)">{fmtFull(total)}</text>
        </svg>
        {active !== null && <Tooltip px={c} width={S * 2} title={null} rows={row(slices[active]).map(r => ({ ...r, color: SERIES_COLORS[active] }))} />}
      </div>
      <Legend items={slices.map(s => `${s.label} ${pct(s.value, total)}`)} />
    </div>
  )
}

// sparkline: a small line, end-dot on the last value; no axes.
export function Sparkline({ data, label }) {
  useTranslation() // renders again on a language change
  const { data: v, unit } = data
  const W = 140, H = 32, p = 4
  const lo = Math.min(...v), hi = Math.max(...v), span = hi - lo || 1
  const pts = v.map((d, i) => [p + ((W - 2 * p) * i) / (v.length - 1), H - p - ((H - 2 * p) * (d - lo)) / span])
  const last = pts[pts.length - 1]
  return (
    <div tabIndex={0} role="img" aria-label={i18nOr('ui7Chat.sparkline_label', { label, latest: fmtFull(v[v.length - 1], unit), lo: fmtFull(lo, unit), hi: fmtFull(hi, unit) })} style={{ display: 'inline-flex', alignItems: 'center', gap: 8, outlineOffset: 2, borderRadius: 4 }}>
      <svg width={W} height={H} aria-hidden="true">
        <polyline points={pts.map(q => q.join(',')).join(' ')} fill="none" stroke={SERIES_COLORS[0]} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
        <circle cx={last[0]} cy={last[1]} r={4} fill={SERIES_COLORS[0]} stroke={SURFACE} strokeWidth={2} />
      </svg>
      <span style={{ fontSize: 13, color: COLOR.text }}>{fmtFull(v[v.length - 1], unit)}</span>
    </div>
  )
}

const TREND = { up: ArrowUp, down: ArrowDown, flat: Minus }

// stat tiles: label, big proportional value, optional delta with a direction
// icon (direction is shown by icon and word, never by color alone).
export function StatTiles({ data }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))', gap: 8 }}>
      {data.stats.map((s, i) => {
        const Icon = s.trend ? TREND[s.trend] : null
        return (
          <div key={i} tabIndex={0} role="group" aria-label={`${s.label}: ${s.value}${s.delta ? `, ${s.delta}` : ''}`}
            style={{ border: `1px solid ${COLOR.border}`, borderRadius: 6, padding: '6px 8px', outlineOffset: 2 }}>
            <div style={{ fontSize: 10, color: COLOR.muted }}>{s.label}</div>
            <div style={{ fontSize: 20, fontWeight: 600, color: COLOR.text, lineHeight: 1.2, wordBreak: 'break-word' }}>{typeof s.value === 'number' ? fmtFull(s.value) : s.value}</div>
            {(s.delta || s.hint) && (
              <div style={{ fontSize: 10, color: COLOR.muted, display: 'flex', alignItems: 'center', gap: 3, flexWrap: 'wrap' }}>
                {Icon && <Icon size={10} aria-hidden="true" />}{s.delta}{s.hint && <span> {s.hint}</span>}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
