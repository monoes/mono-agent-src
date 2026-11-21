import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Table2, BarChart3 } from 'lucide-react'
import { panel, heading, COLOR } from './ui.js'
import { fmtFull } from './chartScale.js'
import { CartesianChart } from './CartesianChart.jsx'
import { DonutChart, Sparkline, StatTiles } from './MiniCharts.jsx'
import { Legend } from './chartParts.jsx'

const cell = { padding: '2px 8px', textAlign: 'right', fontVariantNumeric: 'tabular-nums', borderBottom: `1px solid ${COLOR.border}` }

// The same data as a plain table (the accessible alternative to the plot).
function DataTable({ data, caption }) {
  const { t } = useTranslation()
  const { type, unit } = data
  let head, body
  if (type === 'donut') {
    head = [t('chatBlocks.chart.label'), t('chatBlocks.chart.value')]
    body = data.slices.map(s => [s.label, fmtFull(s.value, unit)])
  } else if (type === 'sparkline') {
    head = ['#', t('chatBlocks.chart.value')]
    body = data.data.map((v, i) => [String(i + 1), fmtFull(v, unit)])
  } else {
    head = ['', ...data.series.map(s => s.name)]
    body = data.x.map((l, i) => [l, ...data.series.map(s => fmtFull(s.data[i], unit))])
  }
  return (
    <div style={{ overflowX: 'auto', maxHeight: 240, overflowY: 'auto' }}>
      <table style={{ borderCollapse: 'collapse', fontSize: 10, width: '100%' }}>
        <caption style={{ ...srOnlyCaption }}>{caption}</caption>
        <thead><tr>{head.map((h, i) => <th key={i} scope="col" style={{ ...cell, textAlign: i === 0 ? 'left' : 'right', color: COLOR.muted, fontWeight: 600 }}>{h}</th>)}</tr></thead>
        <tbody>{body.map((r, i) => <tr key={i}>{r.map((c, j) => (j === 0 ? <th key={j} scope="row" style={{ ...cell, textAlign: 'left', fontWeight: 400 }}>{c}</th> : <td key={j} style={cell}>{c}</td>))}</tr>)}</tbody>
      </table>
    </div>
  )
}
const srOnlyCaption = { position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }

// chart: bar / line / area / donut / sparkline plots and stat tiles.
export function ChartBlock({ data }) {
  const { t } = useTranslation()
  const [asTable, setAsTable] = useState(false)
  const label = data.title || t(`chatBlocks.chart.type.${data.type}`)
  const plotted = data.type !== 'stat'
  const multi = data.series && data.series.length > 1
  return (
    <div style={panel}>
      {(data.title || plotted) && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
          <p style={{ ...heading, margin: 0, flex: 1 }}>{data.title}</p>
          {plotted && (
            <button type="button" aria-pressed={asTable} onClick={() => setAsTable(v => !v)} title={asTable ? t('chatBlocks.chart.showChart') : t('chatBlocks.chart.showTable')}
              style={{ display: 'inline-flex', alignItems: 'center', gap: 4, background: 'transparent', border: `1px solid ${COLOR.border}`, borderRadius: 4, padding: '1px 6px', color: COLOR.muted, fontFamily: 'var(--font-mono)', fontSize: 9, cursor: 'pointer' }}>
              {asTable ? <BarChart3 size={10} /> : <Table2 size={10} />}{asTable ? t('chatBlocks.chart.chart') : t('chatBlocks.chart.table')}
            </button>
          )}
        </div>
      )}
      {asTable && plotted ? <DataTable data={data} caption={label} />
        : data.type === 'stat' ? <StatTiles data={data} />
          : data.type === 'donut' ? <DonutChart data={data} label={label} />
            : data.type === 'sparkline' ? <Sparkline data={data} label={label} />
              : <CartesianChart data={data} label={label} />}
      {multi && !asTable && <Legend items={data.series.map(s => s.name)} />}
    </div>
  )
}
