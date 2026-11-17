import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowUp, ArrowDown } from 'lucide-react'
import { panel, heading, COLOR } from './ui.js'
import { sortRows } from './tableSchema.js'
import { AppLinkButton } from './AppLinkButton.jsx'

const pad = { padding: '3px 8px', borderBottom: `1px solid ${COLOR.border}` }
const show = (v) => (v === null ? '' : typeof v === 'boolean' ? (v ? '✓' : '✗') : String(v))

// table: click a column header to sort (asc, desc, back to given order).
export function TableBlock({ data }) {
  const { t } = useTranslation()
  const [sort, setSort] = useState(null)
  const rows = useMemo(() => (sort ? sortRows(data.rows, sort.col, sort.dir) : data.rows), [data.rows, sort])
  const hasLinks = data.rows.some(r => r.link)
  const next = (c) => setSort(s => (!s || s.col !== c ? { col: c, dir: 'asc' } : s.dir === 'asc' ? { col: c, dir: 'desc' } : null))
  return (
    <div style={panel}>
      {data.title && <p style={heading}>{data.title}</p>}
      <div style={{ overflowX: 'auto', maxHeight: 320, overflowY: 'auto' }}>
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 11 }}>
          <thead>
            <tr>
              {data.columns.map((c, ci) => {
                const on = sort?.col === ci
                return (
                  <th key={c.key} scope="col" aria-sort={on ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'} style={{ ...pad, textAlign: c.align, position: 'sticky', top: 0, background: COLOR.surface }}>
                    <button type="button" onClick={() => next(ci)} style={{ background: 'transparent', border: 'none', padding: 0, cursor: 'pointer', color: COLOR.muted, fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 3 }}>
                      {c.label}{on && (sort.dir === 'asc' ? <ArrowUp size={9} aria-hidden="true" /> : <ArrowDown size={9} aria-hidden="true" />)}
                    </button>
                  </th>
                )
              })}
              {hasLinks && <th style={pad}><span style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden' }}>{t('chatBlocks.open')}</span></th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, ri) => (
              <tr key={ri}>
                {r.cells.map((v, ci) => (
                  <td key={ci} style={{ ...pad, textAlign: data.columns[ci].align, fontVariantNumeric: typeof v === 'number' ? 'tabular-nums' : undefined, wordBreak: 'break-word' }}>{show(v)}</td>
                ))}
                {hasLinks && <td style={{ ...pad, textAlign: 'right' }}>{r.link && <AppLinkButton href={r.link} />}</td>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
