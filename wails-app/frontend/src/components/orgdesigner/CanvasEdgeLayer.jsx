import { SectionEdges } from './SectionLayer.jsx'
import { useTranslation } from 'react-i18next'

// The SVG edge layer: reports-to edges, document edges, the pending edge and live message edges.
export default function CanvasEdgeLayer({ camera, edges, sections, rects, docEdges, secDragPending, readOnly, onRemoveDocEdge, pendingPath, pendingColor, liveEdges }) {
  const { t } = useTranslation()
  return (
            <svg style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', overflow: 'visible', zIndex: 1, pointerEvents: 'none' }}>
              <g transform={`translate(${camera.x} ${camera.y}) scale(${camera.zoom})`}>
                {edges.map(ep => (
                  <g key={ep.id}>
                    <path d={ep.path} stroke={ep.color} strokeWidth={ep.width} fill="none" strokeOpacity={0.65} />
                    <path d={ep.path} stroke={ep.color} strokeWidth={4} fill="none" strokeOpacity={0} style={{ pointerEvents: 'stroke' }} />
                  </g>
                ))}
                {sections.length > 0 && (
                  <SectionEdges rects={rects} edges={docEdges} pending={secDragPending} readOnly={readOnly} onRemove={onRemoveDocEdge} />
                )}
                {pendingPath && (
                  <path d={pendingPath} stroke={pendingColor} strokeWidth={1.8} fill="none" strokeDasharray="5 4" strokeOpacity={0.8} />
                )}
                {liveEdges.map(e => (
                  <g key={`live:${e.from}->${e.to}`} data-testid="live-edge" data-from={e.from} data-to={e.to} data-recent={e.recent ? '1' : '0'}>
                    <path
                      d={e.path}
                      stroke={e.recent ? 'var(--green-neon, #22c55e)' : 'rgba(148,163,184,0.5)'}
                      strokeWidth={e.recent ? 2.2 : 1}
                      strokeOpacity={e.recent ? 0.9 : 0.25}
                      strokeDasharray={e.recent ? '8 6' : '3 5'}
                      fill="none"
                      className={e.recent ? 'od-live-edge' : undefined}
                    />
                    <title>{`${t('ui6Orgs.arc_title', { from: e.from, to: e.to, count: e.count })}${e.subject ? t('ui6Orgs.arc_last', { subject: e.subject }) : ''}`}</title>
                  </g>
                ))}
              </g>
            </svg>
  )
}
