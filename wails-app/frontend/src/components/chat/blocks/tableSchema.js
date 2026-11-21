import { obj, str, optStr, optLink, opt, arr, oneOf, unique, bad } from './schema.js'

export const MAX_COLUMNS = 8
export const MAX_ROWS = 100
const KEY = /^[A-Za-z][A-Za-z0-9_]{0,31}$/

function cell(v) {
  if (v === undefined || v === null) return null
  if (typeof v === 'number') return Number.isFinite(v) ? v : bad('finite number expected')
  if (typeof v === 'boolean') return v
  if (typeof v === 'string') return v.length > 200 ? bad('string too long') : v
  return bad('cell type')
}

// table: columns + rows; rows are normalized to arrays aligned with columns.
export function validateTable(j) {
  const o = obj(j)
  const columns = arr(o.columns, 1, MAX_COLUMNS).map((c) => {
    const p = obj(c)
    const align = opt(p, 'align')
    return { key: str(p.key, { re: KEY, max: 32 }), label: str(p.label, { max: 60 }), align: align === undefined ? 'left' : oneOf(align, ['left', 'right']) }
  })
  unique(columns, 'key')
  if (columns.some(c => c.key === 'link')) bad('reserved key')
  const rows = arr(o.rows, 1, MAX_ROWS).map((r) => {
    const p = obj(r)
    return { cells: columns.map(c => cell(Object.prototype.hasOwnProperty.call(p, c.key) ? p[c.key] : null)), link: optLink(p, 'link') }
  })
  return { title: optStr(o, 'title'), columns, rows }
}

// compareCells: numbers numerically, everything else as text; empty last.
export function compareCells(a, b) {
  if (a === null || b === null) return a === b ? 0 : a === null ? 1 : -1
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' })
}

// sortRows: stable sort of rows by column index; dir 'asc' | 'desc' (empties stay last).
export function sortRows(rows, col, dir) {
  const sign = dir === 'desc' ? -1 : 1
  return rows.map((r, i) => [r, i]).sort(([a, ia], [b, ib]) => {
    const x = a.cells[col], y = b.cells[col]
    if (x === null || y === null) return compareCells(x, y) || ia - ib
    return sign * compareCells(x, y) || ia - ib
  }).map(([r]) => r)
}
