import { useEffect, useMemo, useRef, useState } from 'react'
import { Search } from 'lucide-react'
import { loadIconManifest, iconUrl, CAT_COLOR, pushRecentIcon } from './roleIcons.js'
import { useTranslation } from 'react-i18next'

const COLS = 8
const TILE = 56

/**
 * IconPickerModal — full-featured picker over all 118 vendored role
 * archetype icons. A single instance of this modal should exist per
 * Org Designer session; RoleInspector (and anything else that needs to
 * change a role's icon) delegates to it via `onOpenIconPicker()` rather
 * than embedding its own copy, so only one is ever mounted at a time.
 *
 * @param {boolean} open
 * @param {string} [currentIconId] Icon id to ring/highlight as "currently selected".
 * @param {(iconId: string) => void} onSelect Called once, with the chosen icon id,
 *   right before `onClose()`. This component calls `pushRecentIcon(id)` itself
 *   before invoking `onSelect`, so callers don't need to.
 * @param {() => void} onClose Called after a selection, or on Escape / backdrop click.
 */
export default function IconPickerModal({ open, currentIconId, onSelect, onClose }) {
  const { t } = useTranslation()
  const [manifest, setManifest] = useState([])
  const [search, setSearch] = useState('')
  const [categoryFilter, setCategoryFilter] = useState(null)
  const [focusedIndex, setFocusedIndex] = useState(0)
  const [hoveredIndex, setHoveredIndex] = useState(null)
  const searchRef = useRef(null)
  const tileRefs = useRef([])

  useEffect(() => {
    if (!open) return
    loadIconManifest().then(setManifest)
    setSearch('')
    setCategoryFilter(null)
    setTimeout(() => searchRef.current?.focus(), 30)
  }, [open])

  const categories = useMemo(
    () => [...new Set(manifest.map(a => a.category))].sort(),
    [manifest]
  )

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return manifest.filter(item => {
      if (categoryFilter && item.category !== categoryFilter) return false
      if (!q) return true
      return item.label.toLowerCase().includes(q) || item.id.toLowerCase().includes(q) || item.category.toLowerCase().includes(q)
    })
  }, [manifest, search, categoryFilter])

  // Keep focusedIndex in range and, on open/filter-change, park it on the
  // current selection if present so arrow-key nav starts somewhere sensible.
  useEffect(() => {
    if (!open) return
    const idx = currentIconId ? filtered.findIndex(i => i.id === currentIconId) : -1
    setFocusedIndex(idx >= 0 ? idx : 0)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, manifest, search, categoryFilter])

  useEffect(() => {
    if (!open || !filtered.length) return
    const clamped = Math.min(Math.max(focusedIndex, 0), filtered.length - 1)
    tileRefs.current[clamped]?.scrollIntoView({ block: 'nearest' })
  }, [focusedIndex, filtered.length, open])

  const commitSelect = (item) => {
    if (!item) return
    pushRecentIcon(item.id)
    onSelect(item.id)
    onClose()
  }

  useEffect(() => {
    if (!open) return
    const onKey = (e) => {
      if (e.key === 'Escape') { onClose(); return }
      if (!filtered.length) return
      if (e.key === 'Enter') { e.preventDefault(); commitSelect(filtered[Math.min(Math.max(focusedIndex, 0), filtered.length - 1)]); return }
      let delta = null
      if (e.key === 'ArrowRight') delta = 1
      else if (e.key === 'ArrowLeft') delta = -1
      else if (e.key === 'ArrowDown') delta = COLS
      else if (e.key === 'ArrowUp') delta = -COLS
      if (delta == null) return
      e.preventDefault()
      setFocusedIndex(prev => Math.min(Math.max(prev + delta, 0), filtered.length - 1))
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, filtered, focusedIndex])

  if (!open) return null

  const captionItem = filtered[hoveredIndex ?? focusedIndex]

  return (
    <div
      className="modal-overlay"
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div role="dialog" aria-modal="true" aria-label={t('ui6OrgsCanvas.choose_icon_label')} className="modal" style={{ width: 640 }}>
        <div className="modal-title">{t('ui6OrgsCanvas.choose_icon')}</div>

        <div style={{ position: 'relative', marginBottom: 10 }}>
          <Search size={12} style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)', pointerEvents: 'none' }} />
          <input
            ref={searchRef}
            className="form-input"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder={t('ui6OrgsCanvas.search_icons')}
            style={{ paddingLeft: 30 }}
          />
        </div>

        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 12 }}>
          {categories.map(cat => {
            const active = categoryFilter === cat
            const color = CAT_COLOR[cat] || '#8899aa'
            return (
              <button
                key={cat}
                onClick={() => setCategoryFilter(active ? null : cat)}
                style={{
                  padding: '3px 10px', borderRadius: 999, cursor: 'pointer',
                  fontFamily: 'var(--font-mono)', fontSize: 10, letterSpacing: 0.5,
                  background: active ? `${color}26` : 'transparent',
                  border: `1px solid ${active ? color : 'var(--border)'}`,
                  color: active ? color : 'var(--text-secondary)',
                }}
              >
                {cat}
              </button>
            )
          })}
        </div>

        <div
          style={{
            display: 'grid', gridTemplateColumns: `repeat(${COLS}, ${TILE}px)`, gap: 8,
            maxHeight: 360, overflowY: 'auto', padding: 2, justifyContent: 'center',
          }}
        >
          {filtered.map((item, i) => {
            const isFocused = i === focusedIndex
            const isCurrent = item.id === currentIconId
            return (
              <div
                key={item.id}
                ref={el => { tileRefs.current[i] = el }}
                onMouseEnter={() => setHoveredIndex(i)}
                onMouseLeave={() => setHoveredIndex(null)}
                onClick={() => commitSelect(item)}
                title={item.label}
                style={{
                  width: TILE, height: TILE, borderRadius: 8, cursor: 'pointer',
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                  background: isFocused ? 'rgba(0,180,216,0.1)' : 'transparent',
                  border: isCurrent
                    ? '2px solid #00b4d8'
                    : isFocused ? '1px solid rgba(0,180,216,0.4)' : '1px solid transparent',
                  boxShadow: isCurrent ? '0 0 0 3px rgba(0,180,216,0.18)' : 'none',
                }}
              >
                <img src={iconUrl(item.id)} loading="lazy" alt="" style={{ width: 40, height: 40, borderRadius: '50%' }} />
              </div>
            )
          })}
          {filtered.length === 0 && (
            <div style={{ gridColumn: `1 / -1`, textAlign: 'center', padding: 24, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 11 }}>
              {t('ui6OrgsCanvas.no_icons')}
            </div>
          )}
        </div>

        <div style={{ minHeight: 32, marginTop: 10, display: 'flex', alignItems: 'center', gap: 8, fontFamily: 'var(--font-mono)' }}>
          {captionItem && (
            <>
              <span style={{ fontSize: 12, color: 'var(--text)' }}>{captionItem.label}</span>
              <span style={{ fontSize: 10, color: CAT_COLOR[captionItem.category] || 'var(--text-muted)' }}>{captionItem.category}</span>
              <span style={{ fontSize: 10, color: 'var(--text-dim)' }}>{captionItem.id}</span>
            </>
          )}
        </div>

        <div className="modal-actions">
          <button className="btn btn-ghost" onClick={onClose}>{t('ui6OrgsCanvas.cancel')}</button>
        </div>
      </div>
    </div>
  )
}
