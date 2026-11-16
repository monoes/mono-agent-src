import { obj, str, optStr, opt, num, oneOf, arr, bad } from './schema.js'

export const CHART_TYPES = ['bar', 'line', 'area', 'donut', 'sparkline', 'stat']
export const MAX_POINTS = 60
export const MAX_SERIES = 6
export const MAX_SLICES = 8
export const MAX_STATS = 6

// chart: see the spec. Returns normalized data; throws Bad on anything else.
export function validateChart(j) {
  const o = obj(j)
  const type = oneOf(o.type, CHART_TYPES)
  const base = { type, title: optStr(o, 'title'), unit: optStr(o, 'unit', { max: 24 }) }
  if (type === 'bar' || type === 'line' || type === 'area') {
    const x = arr(o.x, 1, MAX_POINTS).map(v => str(v, { max: 40 }))
    const series = arr(o.series, 1, MAX_SERIES).map((s) => {
      const p = obj(s)
      const data = arr(p.data, x.length, x.length).map(num)
      return { name: str(p.name, { max: 40 }), data }
    })
    return { ...base, x, series }
  }
  if (type === 'donut') {
    const slices = arr(o.slices, 1, MAX_SLICES).map((s) => {
      const p = obj(s)
      const value = num(p.value)
      if (value < 0) bad('negative slice')
      return { label: str(p.label, { max: 40 }), value }
    })
    if (!(slices.reduce((a, s) => a + s.value, 0) > 0)) bad('empty donut')
    return { ...base, slices }
  }
  if (type === 'sparkline') return { ...base, data: arr(o.data, 2, MAX_POINTS).map(num) }
  const stats = arr(o.stats, 1, MAX_STATS).map((s) => {
    const p = obj(s)
    const v = p.value
    const trend = opt(p, 'trend')
    return {
      label: str(p.label, { max: 60 }),
      value: typeof v === 'number' ? num(v) : str(v, { max: 40 }),
      delta: optStr(p, 'delta', { max: 40 }),
      trend: trend === undefined ? undefined : oneOf(trend, ['up', 'down', 'flat']),
      hint: optStr(p, 'hint'),
    }
  })
  return { ...base, stats }
}
