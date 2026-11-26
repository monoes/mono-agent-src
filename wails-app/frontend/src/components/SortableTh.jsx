import { useTranslation } from 'react-i18next'
import { ChevronUp, ChevronDown, ChevronsUpDown } from 'lucide-react'
import t from '../lib/i18nOr.js'

// A table header cell that sorts by sortKey when clicked. The active column
// shows its direction; the others show a faint two-way chevron so it is
// clear they can be clicked. aria-sort carries the same state for screen
// readers.
export default function SortableTh({ label, sortKey, sort, onSort, style }) {
  useTranslation()
  const active = sort.key === sortKey
  const Icon = !active ? ChevronsUpDown : sort.dir === 'asc' ? ChevronUp : ChevronDown
  return (
    <th
      style={{ padding: '6px 8px', ...style }}
      aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
    >
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        title={t('ui6PagesViewers.sort_by', { column: label.toLowerCase() })}
        style={{
          background: 'none', border: 'none', padding: 0, cursor: 'pointer',
          color: active ? '#00b4d8' : 'inherit', font: 'inherit',
          textTransform: 'inherit', letterSpacing: 'inherit',
          display: 'inline-flex', alignItems: 'center', gap: 3,
        }}
      >
        {label}
        <Icon size={11} style={{ opacity: active ? 1 : 0.4 }} aria-hidden="true" />
      </button>
    </th>
  )
}
