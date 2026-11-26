// Section containers and document edges for the Org Designer canvas, plus
// the pointer interactions they own (move a whole section by its header,
// draw a document edge from one header to another). Presentational: every
// finished interaction is reported upward; OrgDesigner calls the backend.
//
//   <SectionBoxes rects selectedName leadIds onStart* onSelect />   HTML, in the node layer
//   <SectionEdges rects edges pending onRemove />                  SVG, inside the camera <g>
//   useSectionDrags(...)                                           the two drags

import { useCallback, useEffect, useRef, useState } from 'react'
import { Link2 } from 'lucide-react'
import { sectionEdgePath, SECTION_HEADER } from './sectionsGraph.js'
import { budgetLine, budgetBadge, budgetTitle } from './budgetModel.js'
import { useTranslation } from 'react-i18next'

export function SectionBoxes({ rects, sections, selectedName, readOnly, onSelect, onStartMove, onStartEdge, hoverEdgeTarget, budgets = {}, staleRates = '' }) {
  const { t } = useTranslation()
  return rects.map(r => {
    const s = sections.find(x => x.name === r.name)
    const selected = selectedName === r.name
    const target = hoverEdgeTarget === r.name
    const live = budgets[r.name]
    const badge = budgetBadge(live)
    return (
      <div
        key={r.name}
        data-testid={`section-${r.name}`}
        data-od-section={r.name}
        style={{
          position: 'absolute', left: r.x, top: r.y, width: r.w, height: r.h, boxSizing: 'border-box',
          border: `${selected || target ? 2 : 1.5}px dashed ${r.color}`, borderRadius: 14,
          background: `${r.color}${selected ? '1f' : '12'}`, pointerEvents: 'none',
        }}
      >
        <div
          data-testid={`section-header-${r.name}`}
          onMouseDown={(e) => { if (e.button !== 0) return; e.stopPropagation(); onSelect?.(r.name); if (!readOnly) onStartMove?.(r.name, e) }}
          style={{
            height: SECTION_HEADER, display: 'flex', alignItems: 'center', gap: 8, padding: '0 10px',
            pointerEvents: 'auto', cursor: readOnly ? 'default' : 'grab', userSelect: 'none', whiteSpace: 'nowrap', overflow: 'hidden',
            fontFamily: 'var(--font-mono)', fontSize: 11, color: r.color,
          }}
        >
          <strong style={{ letterSpacing: 0.4 }}>{r.name}</strong>
          {s && !live && <span style={{ color: 'var(--text-muted)', flexShrink: 0 }}>{t('ui6OrgsCanvas.lead_line', { id: s.leadId || '—' })}</span>}
          {live
            ? (
              <span data-testid={`section-budget-${r.name}`} title={`${budgetTitle(live)}${staleRates ? ` · ${staleRates}` : ''}`}
                style={{ display: 'flex', gap: 4, minWidth: 0, color: badge?.color || 'var(--text-muted)' }}>
                <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>{budgetLine(live).replace(' / ', '/')}</span>
                {badge && <strong data-testid={`section-budget-badge-${r.name}`} style={{ flexShrink: 0 }}>· {badge.label}</strong>}
                {staleRates && <em data-testid={`section-stale-rates-${r.name}`} aria-label={t('ui6OrgsCanvas.stale_rates')} title={t('ui6OrgsCanvas.stale_rates')} style={{ flexShrink: 0, opacity: 0.8 }}>≈</em>}
              </span>
            )
            : s?.budgetUsd != null && <span style={{ color: 'var(--text-muted)' }}>${s.budgetUsd}</span>}
          <span style={{ flex: 1 }} />
          {!readOnly && (
            <button
              type="button"
              data-testid={`section-edge-handle-${r.name}`}
              title={t('ui6OrgsCanvas.edge_handle_title')}
              aria-label={t('ui6OrgsCanvas.edge_handle_label', { name: r.name })}
              onMouseDown={(e) => { e.stopPropagation(); e.preventDefault(); onStartEdge?.(r.name, e) }}
              style={{ pointerEvents: 'auto', cursor: 'crosshair', background: 'transparent', border: `1px solid ${r.color}`, color: r.color, borderRadius: 6, padding: '1px 5px', display: 'flex' }}
            >
              <Link2 size={11} />
            </button>
          )}
        </div>
      </div>
    )
  })
}

export function SectionEdges({ rects, edges, pending, readOnly, onRemove }) {
  const { t } = useTranslation()
  const byName = new Map(rects.map(r => [r.name, r]))
  const seen = new Map()
  return (
    <g>
      <defs>
        <marker id="od-doc-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" fill="#e2e8f0" />
        </marker>
      </defs>
      {edges.map(e => {
        const a = byName.get(e.from), b = byName.get(e.to)
        if (!a || !b) return null
        const key = [e.from, e.to].sort().join('|')
        const n = seen.get(key) || 0
        seen.set(key, n + 1)
        const { d, mid } = sectionEdgePath(a, b, n * 22)
        const w = 18 + e.type.length * 6.2
        return (
          <g key={`${e.type}:${e.from}->${e.to}`} data-testid="doc-edge" data-type={e.type} data-from={e.from} data-to={e.to}>
            <path d={d} stroke="#e2e8f0" strokeWidth={1.8} fill="none" strokeOpacity={0.8} markerEnd="url(#od-doc-arrow)" />
            <g transform={`translate(${mid.x - w / 2} ${mid.y - 10})`} style={{ pointerEvents: 'auto' }}>
              <rect width={w + 16} height={20} rx={10} fill="var(--bg-card, #0b1220)" stroke="#e2e8f0" strokeOpacity={0.6} />
              <text x={9} y={14} fontSize={10.5} fontFamily="var(--font-mono)" fill="#e2e8f0">{e.type}</text>
              {!readOnly && (
                <g data-testid="doc-edge-remove" onMouseDown={(ev) => { ev.stopPropagation(); onRemove?.(e) }} style={{ cursor: 'pointer' }}>
                  <title>{t('ui6OrgsCanvas.edge_remove_title')}</title>
                  <text x={w + 2} y={14} fontSize={12} fill="#f87171">×</text>
                </g>
              )}
            </g>
          </g>
        )
      })}
      {pending && (
        <path d={`M${pending.sx},${pending.sy} L${pending.tx},${pending.ty}`} stroke={pending.target ? 'var(--green)' : '#e2e8f0'} strokeWidth={1.8} strokeDasharray="5 4" fill="none" />
      )}
    </g>
  )
}

/**
 * The two header drags. `nodesRef`/`cameraRef` are OrgCanvas's refs.
 * Moving a section reports every member's new position through
 * onNodesChange (so the usual debounced layout save persists it); drawing an
 * edge reports onAddEdge(from, to) on release over another section's header.
 */
export function useSectionDrags({ wrapperRef, nodesRef, cameraRef, sectionsRef, onNodesChange, onNodeDragStart, onNodeDragEnd, onAddEdge }) {
  const dragRef = useRef(null)
  const [pending, setPending] = useState(null)
  const hoverRef = useRef(null)

  const world = useCallback((cx, cy) => {
    const rect = wrapperRef.current?.getBoundingClientRect() || { left: 0, top: 0 }
    const cam = cameraRef.current
    return { x: (cx - rect.left - cam.x) / cam.zoom, y: (cy - rect.top - cam.y) / cam.zoom }
  }, [wrapperRef, cameraRef])

  const startMove = useCallback((name, e) => {
    const s = sectionsRef.current.find(x => x.name === name)
    if (!s) return
    const members = nodesRef.current.filter(n => s.roster.includes(n.id)).map(n => ({ id: n.id, x: n.x, y: n.y }))
    dragRef.current = { type: 'move', members, startX: e.clientX, startY: e.clientY }
    onNodeDragStart?.(null, e)
  }, [nodesRef, sectionsRef, onNodeDragStart])

  const startEdge = useCallback((name, e) => {
    const p = world(e.clientX, e.clientY)
    dragRef.current = { type: 'edge', from: name }
    hoverRef.current = null
    onNodeDragStart?.(null, e)
    setPending({ sx: p.x, sy: p.y, tx: p.x, ty: p.y, target: null })
  }, [world, onNodeDragStart])

  useEffect(() => {
    const onMove = (e) => {
      const d = dragRef.current
      if (!d) return
      if (d.type === 'move') {
        const z = cameraRef.current.zoom
        const dx = (e.clientX - d.startX) / z, dy = (e.clientY - d.startY) / z
        const at = new Map(d.members.map(m => [m.id, m]))
        onNodesChange?.(nodesRef.current.map(n => (at.has(n.id) ? { ...n, x: at.get(n.id).x + dx, y: at.get(n.id).y + dy } : n)))
      } else {
        const p = world(e.clientX, e.clientY)
        const hit = document.elementFromPoint(e.clientX, e.clientY)?.closest?.('[data-od-section]')?.dataset?.odSection || null
        hoverRef.current = hit && hit !== d.from ? hit : null
        setPending(prev => (prev ? { ...prev, tx: p.x, ty: p.y, target: hoverRef.current } : prev))
      }
    }
    const onUp = () => {
      const d = dragRef.current
      if (!d) return
      dragRef.current = null
      if (d.type === 'edge') {
        setPending(null)
        if (hoverRef.current) onAddEdge?.(d.from, hoverRef.current)
      }
      hoverRef.current = null
      onNodeDragEnd?.(null)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    return () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
  }, [world, cameraRef, nodesRef, onNodesChange, onNodeDragEnd, onAddEdge])

  return { startMove, startEdge, pending }
}
