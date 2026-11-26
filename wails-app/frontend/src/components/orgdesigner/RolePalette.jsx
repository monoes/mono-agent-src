import { useEffect, useMemo, useState } from 'react'
import { Search, ChevronDown, ChevronRight, LayoutGrid, List, Clock } from 'lucide-react'
import { loadIconManifest, iconUrl, CAT_COLOR, getRecentIcons, pushRecentIcon } from './roleIcons.js'
import { useTranslation } from 'react-i18next'

const OPEN_KEY = 'od-palette-open'
const VIEW_KEY = 'od-palette-view'

/**
 * RolePalette — the drag-source / quick-add sidebar for the Org Designer
 * canvas, listing all 118 vendored role archetype icons grouped by
 * category. Mirrors NodeRunner.jsx's `Palette` component (search, per-
 * category open/close persisted to localStorage, mousedown-starts-drag +
 * click-quick-adds) adapted for role archetypes instead of workflow nodes.
 *
 * This component is a pure "leaf" — it owns no canvas/graph state. It only
 * loads the icon manifest and reports user intent upward via props.
 *
 * @param {(archetypeItem: {id:string,label:string,category:string,icon:string,file:string,index:number}, event: MouseEvent) => void} onDragStart
 *   Called on `mousedown` over a palette row/tile. The parent (OrgCanvas)
 *   owns the actual ghost-drag mechanics; this only needs to hand off the
 *   picked archetype item and the originating mouse event.
 * @param {(archetypeItem: object) => void} onQuickAdd
 *   Called on `click` (not drag) — parent should add a new role node at a
 *   default canvas location using this archetype's suggested icon/type.
 */
export default function RolePalette({ onDragStart, onQuickAdd }) {
  const { t } = useTranslation()
  const [manifest, setManifest] = useState([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [recentIds, setRecentIds] = useState(() => getRecentIcons())
  const [view, setView] = useState(() => {
    try { return localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'grid' } catch { return 'grid' }
  })
  const [open, setOpen] = useState(() => {
    try { return JSON.parse(localStorage.getItem(OPEN_KEY) || '{}') } catch { return {} }
  })

  useEffect(() => {
    let cancelled = false
    loadIconManifest().then(items => { if (!cancelled) setManifest(items) }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  const setViewPersist = (v) => {
    setView(v)
    try { localStorage.setItem(VIEW_KEY, v) } catch {}
  }

  const toggle = (cat) => setOpen(prev => {
    const next = { ...prev, [cat]: !(prev[cat] !== false) }
    try { localStorage.setItem(OPEN_KEY, JSON.stringify(next)) } catch {}
    return next
  })

  const byId = useMemo(() => new Map(manifest.map(a => [a.id, a])), [manifest])

  const q = search.trim().toLowerCase()
  const matches = (item) => !q || item.label.toLowerCase().includes(q) || item.id.toLowerCase().includes(q) || item.category.toLowerCase().includes(q)

  const categories = useMemo(() => {
    const byCat = new Map()
    manifest.forEach(item => {
      if (!byCat.has(item.category)) byCat.set(item.category, [])
      byCat.get(item.category).push(item)
    })
    return [...byCat.entries()]
      .map(([category, items]) => ({ category, items: items.filter(matches) }))
      .filter(c => c.items.length > 0)
      .sort((a, b) => a.category.localeCompare(b.category))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [manifest, q])

  const recentItems = recentIds.map(id => byId.get(id)).filter(Boolean).filter(matches)

  const handleQuickAdd = (item) => {
    onQuickAdd(item)
    pushRecentIcon(item.id)
    setRecentIds(getRecentIcons())
  }
  const handleDragStart = (item, e) => onDragStart(item, e)

  const tileSize = view === 'grid' ? 44 : 24

  const renderRow = (item) => (
    <div
      key={item.id}
      // Deliberately NOT `draggable`/native HTML5 DnD — NodeRunner.jsx's own
      // Workflows palette avoids it too, in favor of the plain mousedown ->
      // document mousemove/mouseup ghost drag below (handleDragStart), which
      // is the pattern proven to work reliably in this Wails/WebKit app.
      // Mixing the two would let a native drag gesture swallow the mouse
      // events this ghost-drag effect needs once it starts.
      onMouseDown={e => { e.preventDefault(); handleDragStart(item, e) }}
      onClick={() => handleQuickAdd(item)}
      title={t('ui6OrgsCanvas.palette_add', { label: item.label })}
      style={view === 'grid' ? {
        display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 4,
        padding: '6px 4px', cursor: 'grab', borderRadius: 6,
        border: '1px solid transparent', transition: 'all 80ms',
      } : {
        display: 'flex', alignItems: 'center', gap: 8,
        padding: '4px 10px', cursor: 'grab', borderRadius: 6,
        border: '1px solid transparent', transition: 'all 80ms',
      }}
      onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.05)'; e.currentTarget.style.borderColor = 'rgba(255,255,255,0.08)' }}
      onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.borderColor = 'transparent' }}
    >
      <img
        src={iconUrl(item.id)}
        loading="lazy"
        alt=""
        draggable={false}
        style={{ width: tileSize, height: tileSize, borderRadius: '50%', flexShrink: 0, background: 'rgba(255,255,255,0.04)', pointerEvents: 'none' }}
      />
      <span style={{
        fontFamily: 'var(--font-mono)', fontSize: view === 'grid' ? 9.5 : 11,
        color: 'var(--text-secondary, #8899aa)', textAlign: view === 'grid' ? 'center' : 'left',
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: view === 'grid' ? 'normal' : 'nowrap',
        lineHeight: 1.25, maxWidth: view === 'grid' ? tileSize + 20 : undefined,
        userSelect: 'none', pointerEvents: 'none',
      }}>
        {item.label}
      </span>
    </div>
  )

  return (
    <div style={{
      width: 220, flexShrink: 0, display: 'flex', flexDirection: 'column',
      background: '#080d16', borderRight: '1px solid rgba(0,180,216,0.1)', overflow: 'hidden',
    }}>
      <div style={{ padding: '10px 10px 8px', borderBottom: '1px solid rgba(0,180,216,0.08)', flexShrink: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div style={{ position: 'relative' }}>
          <Search size={11} style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted, #8492a6)', pointerEvents: 'none' }} />
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder={t('ui6OrgsCanvas.search_roles')}
            style={{
              width: '100%', background: '#020509', border: '1px solid rgba(0,180,216,0.15)',
              borderRadius: 6, padding: '5px 8px 5px 26px', color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 11, outline: 'none', boxSizing: 'border-box',
            }}
          />
        </div>
        <div style={{ display: 'flex', gap: 4 }}>
          <button
            onClick={() => setViewPersist('grid')}
            title={t('ui6OrgsCanvas.grid_view')}
            style={{
              flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4,
              padding: '4px 0', borderRadius: 5, cursor: 'pointer', fontSize: 10,
              fontFamily: 'var(--font-mono)',
              background: view === 'grid' ? 'rgba(0,180,216,0.12)' : 'transparent',
              border: `1px solid ${view === 'grid' ? 'rgba(0,180,216,0.35)' : 'rgba(255,255,255,0.08)'}`,
              color: view === 'grid' ? '#00b4d8' : 'var(--text-muted, #8492a6)',
            }}
          >
            <LayoutGrid size={11} /> {t('ui6OrgsCanvas.grid')}
          </button>
          <button
            onClick={() => setViewPersist('list')}
            title={t('ui6OrgsCanvas.list_view')}
            style={{
              flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4,
              padding: '4px 0', borderRadius: 5, cursor: 'pointer', fontSize: 10,
              fontFamily: 'var(--font-mono)',
              background: view === 'list' ? 'rgba(0,180,216,0.12)' : 'transparent',
              border: `1px solid ${view === 'list' ? 'rgba(0,180,216,0.35)' : 'rgba(255,255,255,0.08)'}`,
              color: view === 'list' ? '#00b4d8' : 'var(--text-muted, #8492a6)',
            }}
          >
            <List size={11} /> {t('ui6OrgsCanvas.list')}
          </button>
        </div>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '4px 0 12px' }}>
        {loading && (
          <div style={{ padding: 14, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted, #8492a6)' }}>
            {t('ui6OrgsCanvas.loading_icons')}
          </div>
        )}

        {!loading && recentItems.length > 0 && (
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 5, padding: '7px 10px 5px' }}>
              <Clock size={9} color="var(--text-muted, #8492a6)" />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted, #8492a6)', letterSpacing: 1.5, textTransform: 'uppercase' }}>
                {t('ui6OrgsCanvas.recent')}
              </span>
            </div>
            <div style={view === 'grid' ? { display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 2, padding: '0 6px 6px' } : { padding: '0 0 4px' }}>
              {recentItems.map(renderRow)}
            </div>
          </div>
        )}

        {!loading && categories.map(({ category, items }) => {
          const isOpen = q ? true : (open[category] !== false)
          const color = CAT_COLOR[category] || '#8899aa'
          return (
            <div key={category}>
              <div
                onClick={() => !q && toggle(category)}
                style={{ display: 'flex', alignItems: 'center', gap: 5, padding: '7px 10px 5px', cursor: q ? 'default' : 'pointer', userSelect: 'none' }}
              >
                {!q && (isOpen ? <ChevronDown size={9} color={color} /> : <ChevronRight size={9} color={color} />)}
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color, letterSpacing: 1.5, textTransform: 'uppercase' }}>
                  {category}
                </span>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 8.5, color: 'var(--text-muted, #8492a6)' }}>({items.length})</span>
              </div>
              {isOpen && (
                <div style={view === 'grid' ? { display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 2, padding: '0 6px 6px' } : { padding: '0 0 4px' }}>
                  {items.map(renderRow)}
                </div>
              )}
            </div>
          )
        })}

        {!loading && categories.length === 0 && recentItems.length === 0 && (
          <div style={{ padding: 14, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted, #8492a6)', textAlign: 'center' }}>
            {t('ui6OrgsCanvas.no_roles')}
          </div>
        )}
      </div>
    </div>
  )
}
