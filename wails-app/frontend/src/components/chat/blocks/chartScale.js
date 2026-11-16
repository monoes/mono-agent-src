// Pure helpers for the SVG charts: ticks, number formatting, geometry.

// Series colors: the dataviz skill's categorical palette, dark-mode steps
// (the chat is dark), in its fixed order; checked with the skill's
// validate_palette.js (dark, 6 slots: all checks pass). Never cycled; a
// chart has at most six series.
export const SERIES_COLORS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#0ca30c']
export const SURFACE = '#0d1a28'
export const GRID = '#1e3a4f'
export const AXIS_TEXT = '#94a3b8'

// niceTicks: about `count` round tick values spanning [lo, hi].
export function niceTicks(lo, hi, count = 4) {
  if (lo === hi) { hi = lo + 1; if (lo > 0) lo = 0 }
  const span = hi - lo
  const raw = span / Math.max(1, count)
  const mag = Math.pow(10, Math.floor(Math.log10(raw)))
  const norm = raw / mag
  const step = (norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10) * mag
  const first = Math.floor(lo / step) * step
  const last = Math.ceil(hi / step) * step
  const ticks = []
  for (let v = first; v <= last + step / 2; v += step) ticks.push(Math.round(v / step) * step)
  return ticks
}

// fmtCompact: axis style: full under 10,000, K/M/B above.
export function fmtCompact(n) {
  const a = Math.abs(n)
  if (a >= 1e9) return `${trim(n / 1e9)}B`
  if (a >= 1e6) return `${trim(n / 1e6)}M`
  if (a >= 1e4) return `${trim(n / 1e3)}K`
  return trim(n)
}
function trim(n) { return String(Math.round(n * 100) / 100) }

export function fmtFull(n, unit) {
  const s = Number.isFinite(n) ? n.toLocaleString(undefined, { maximumFractionDigits: 2 }) : String(n)
  return unit ? `${s} ${unit}` : s
}

// bandIndex: which of n equal bands across [x0, x1] holds x (clamped).
export function bandIndex(x, x0, x1, n) {
  if (n <= 1) return 0
  const i = Math.floor(((x - x0) / (x1 - x0)) * n)
  return Math.max(0, Math.min(n - 1, i))
}

// labelStep: show every k-th x label so they do not collide.
export function labelStep(labels, bandPx) {
  const widest = labels.reduce((m, l) => Math.max(m, l.length), 1) * 5.6 + 8
  return Math.max(1, Math.ceil(widest / Math.max(1, bandPx)))
}

// barPath: a bar from the baseline y0 to y1 with a 4px rounded data end and a
// square baseline end.
export function barPath(x, w, y0, y1) {
  const up = y1 <= y0
  const h = Math.abs(y1 - y0)
  const r = Math.min(4, w / 2, h)
  const top = Math.min(y0, y1)
  if (h === 0) return ''
  if (up) return `M${x},${y0}V${top + r}Q${x},${top} ${x + r},${top}H${x + w - r}Q${x + w},${top} ${x + w},${top + r}V${y0}Z`
  return `M${x},${y0}V${top + h - r}Q${x},${top + h} ${x + r},${top + h}H${x + w - r}Q${x + w},${top + h} ${x + w},${top + h - r}V${y0}Z`
}

// arcPath: a donut slice between angles a0..a1 (radians, 0 = 12 o'clock).
export function arcPath(cx, cy, r0, r1, a0, a1) {
  const p = (r, a) => [cx + r * Math.sin(a), cy - r * Math.cos(a)]
  const large = a1 - a0 > Math.PI ? 1 : 0
  const [x0, y0] = p(r1, a0), [x1, y1] = p(r1, a1), [x2, y2] = p(r0, a1), [x3, y3] = p(r0, a0)
  return `M${x0},${y0}A${r1},${r1} 0 ${large} 1 ${x1},${y1}L${x2},${y2}A${r0},${r0} 0 ${large} 0 ${x3},${y3}Z`
}
