import { useRef, useState } from 'react'
import { SERIES_COLORS, SURFACE, GRID, AXIS_TEXT, niceTicks, fmtCompact, fmtFull, bandIndex, labelStep, barPath } from './chartScale.js'
import { Tooltip, srOnly, useWidth } from './chartParts.jsx'
import { useTranslation } from 'react-i18next'
import i18nOr from '../../../lib/i18nOr.js'

const H = 190
const M = { l: 40, r: 10, t: 10, b: 24 }

// bar / line / area over category labels x. Hand-built SVG; one y axis;
// hover or arrow keys pick a category and show every series' value.
export function CartesianChart({ data, label }) {
  useTranslation() // renders again on a language change
  const { type, x, series, unit } = data
  const wrap = useRef(null)
  const w = useWidth(wrap)
  const [active, setActive] = useState(null)
  const n = x.length
  const iw = w - M.l - M.r
  const ih = H - M.t - M.b
  const band = iw / n

  const all = series.flatMap(s => s.data)
  let lo = Math.min(...all), hi = Math.max(...all)
  if (type !== 'line') { lo = Math.min(0, lo); hi = Math.max(0, hi) }
  const ticks = niceTicks(lo, hi, 4)
  const d0 = ticks[0], d1 = ticks[ticks.length - 1]
  const y = (v) => M.t + ih * (1 - (v - d0) / (d1 - d0))
  const base = y(Math.min(Math.max(0, d0), d1))
  const cx = (i) => M.l + band * (i + 0.5)
  const step = labelStep(x, band)

  const pick = (e) => {
    const r = e.currentTarget.getBoundingClientRect()
    setActive(bandIndex(e.clientX - r.left, M.l, w - M.r, n))
  }
  const onKey = (e) => {
    const k = e.key
    if (k === 'Escape') return setActive(null)
    const move = { ArrowRight: 1, ArrowLeft: -1 }[k]
    if (move === undefined && k !== 'Home' && k !== 'End') return
    e.preventDefault()
    setActive(a => (k === 'Home' ? 0 : k === 'End' ? n - 1 : Math.max(0, Math.min(n - 1, (a ?? (move > 0 ? -1 : n)) + move))))
  }

  const rows = (i) => series.map((s, si) => ({ color: SERIES_COLORS[si], label: s.name, value: fmtFull(s.data[i], unit) }))
  const spoken = active === null ? '' : `${x[active]}: ${series.map(s => `${s.name} ${fmtFull(s.data[active], unit)}`).join(', ')}`
  const k = series.length
  const gw = Math.min(band * 0.8, k * 24 + (k - 1) * 2)
  const bw = Math.min(24, (gw - (k - 1) * 2) / k)

  return (
    <div ref={wrap} style={{ position: 'relative' }}>
      <div tabIndex={0} role="group" aria-roledescription={i18nOr('ui7Chat.chart_role')} aria-label={label}
        onKeyDown={onKey} onFocus={() => setActive(a => a ?? 0)} onBlur={() => setActive(null)}
        style={{ outlineOffset: 2, borderRadius: 4 }}>
        <svg width={w} height={H} onPointerMove={pick} onPointerLeave={() => setActive(null)} aria-hidden="true" style={{ display: 'block', touchAction: 'pan-y' }}>
          {ticks.map(t => (
            <g key={t}>
              <line x1={M.l} x2={w - M.r} y1={y(t)} y2={y(t)} stroke={GRID} strokeWidth={1} />
              <text x={M.l - 6} y={y(t) + 3} textAnchor="end" fontSize={10} fill={AXIS_TEXT} fontFamily="var(--font-mono)">{fmtCompact(t)}</text>
            </g>
          ))}
          {x.map((l, i) => (i % step === 0 ? (
            <text key={i} x={cx(i)} y={H - 7} textAnchor="middle" fontSize={10} fill={AXIS_TEXT} fontFamily="var(--font-mono)">{l}</text>
          ) : null))}
          {active !== null && <rect x={M.l + band * active} y={M.t} width={band} height={ih} fill="rgba(255,255,255,0.05)" />}
          {type === 'bar' && series.map((s, si) => s.data.map((v, i) => (
            <path key={`${si}-${i}`} d={barPath(cx(i) - gw / 2 + si * (bw + 2), bw, base, y(v))} fill={SERIES_COLORS[si]} />
          )))}
          {type !== 'bar' && series.map((s, si) => {
            const pts = s.data.map((v, i) => `${cx(i)},${y(v)}`)
            return (
              <g key={si}>
                {type === 'area' && n > 1 && <polygon points={`${cx(0)},${base} ${pts.join(' ')} ${cx(n - 1)},${base}`} fill={SERIES_COLORS[si]} fillOpacity={0.1} />}
                {n > 1 && <polyline points={pts.join(' ')} fill="none" stroke={SERIES_COLORS[si]} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />}
                {[n - 1, ...(active !== null && active !== n - 1 ? [active] : [])].map(i => (
                  <circle key={i} cx={cx(i)} cy={y(s.data[i])} r={4} fill={SERIES_COLORS[si]} stroke={SURFACE} strokeWidth={2} />
                ))}
              </g>
            )
          })}
        </svg>
      </div>
      <div aria-live="polite" style={srOnly}>{spoken}</div>
      {active !== null && <Tooltip px={cx(active)} width={w} title={x[active]} rows={rows(active)} />}
    </div>
  )
}
