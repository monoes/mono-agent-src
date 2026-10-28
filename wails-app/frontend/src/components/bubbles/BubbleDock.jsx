import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, X, Code2, Network } from 'lucide-react'
import { folderName } from '../chat/useCoderMode.js'
import { layoutBubbles, monogram, totalCost, costEstimated } from '../../lib/coderBubbles.js'
import { OrbitRing } from './OrbitRing.jsx'
import { PersonItemTray } from './PersonItemViews.jsx'
import './bubbles.css'

const statusKey = { idle: 'statusIdle', working: 'statusWorking', done: 'statusDone', error: 'statusError', needs: 'statusNeeds' }

function ChatBubble({ bubble, summary, expanded, onOpen, onClose, onDragStart, onDropOn, dragging, registerEl, titleOf = folderName, orbit = null, side = 'right', onOpenAgent }) {
  const { t } = useTranslation()
  const [hover, setHover] = useState(false)
  // The mouse over a satellite shows that satellite's own card, not the bubble's.
  const [onOrbit, setOnOrbit] = useState(false)
  const hasOrbit = !!orbit && (orbit.satellites.length > 0 || orbit.overflow.length > 0)
  const isOrg = bubble.kind === 'org'
  const name = isOrg ? bubble.orgName : bubble.title || (bubble.cwd ? titleOf(bubble.cwd) : t('bubbles.newChat'))
  const cost = totalCost(summary)
  const estimated = costEstimated(summary)
  const label = (isOrg ? t('orgBubble.bubbleLabel', { name, status: t(`bubbles.${statusKey[summary.status] || 'statusIdle'}`) })
    : t('bubbles.bubbleLabel', { name, status: t(`bubbles.${statusKey[summary.status] || 'statusIdle'}`) })) +
    (summary.unread ? ` · ${t('bubbles.unread', { count: summary.unread })}` : '')
  return (
    <div className={`bubble-wrap${hasOrbit ? ' has-orbit' : ''}`}
      onMouseEnter={() => setHover(true)} onMouseLeave={() => setHover(false)}
      onMouseOver={hasOrbit ? e => setOnOrbit(!!e.target.closest?.('.orbit')) : undefined}
      onDragOver={e => { e.preventDefault(); e.dataTransfer.dropEffect = 'move' }}
      onDrop={e => { e.preventDefault(); onDropOn(bubble.key) }}>
      <button type="button" ref={el => registerEl(bubble.key, el)}
        className={`bubble${expanded ? ' expanded' : ''}${dragging ? ' dragging' : ''}`}
        data-status={summary.status} data-bubble={bubble.key} aria-label={label} aria-pressed={expanded}
        draggable onDragStart={e => { e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', bubble.key); onDragStart(bubble.key) }}
        onDragEnd={() => onDragStart('')}
        onClick={() => onOpen(bubble.key)}>
        <span className="bubble-ring" aria-hidden="true" />
        {bubble.conversationId || isOrg ? monogram(name) : <Code2 size={16} />}
        {isOrg && <span className="bubble-kind" aria-hidden="true"><Network size={8} /></span>}
        {summary.unread > 0 && <span className="bubble-badge" aria-hidden="true">{summary.unread > 9 ? '9+' : summary.unread}</span>}
      </button>
      <button type="button" className="bubble-close" onClick={e => { e.stopPropagation(); onClose(bubble.key) }}
        aria-label={t('bubbles.closeNamed', { name })} title={isOrg ? t('orgBubble.close') : t('bubbles.closeChat')}>
        <X size={9} />
      </button>
      {hasOrbit && (
        <OrbitRing orbit={orbit} variant="arc" facing={side === 'left' ? 0 : 180} label={t('bubbles.orbit.groupNamed', { name })}
          onOpen={c => onOpenAgent?.(bubble, c)} />
      )}
      {hover && !expanded && !onOrbit && (
        <div className="bubble-card" role="tooltip">
          <div className="bubble-card-title">{name}</div>
          {isOrg && <div>{bubble.finished ? t('orgBubble.status.finished') : t('orgBubble.cardKind')}{bubble.runId ? ` · ${t('orgBubble.runLabel', { id: bubble.runId.slice(0, 8) })}` : ''}</div>}
          {bubble.cwd && <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', direction: 'rtl', textAlign: 'left' }}><bdi>{bubble.cwd}</bdi></div>}
          <div style={{ marginTop: 4, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            <span>{t(`bubbles.${statusKey[summary.status] || 'statusIdle'}`)}</span>
            {bubble.model && <span style={{ color: 'var(--cyan)' }}>{bubble.model}</span>}
            {summary.needs > 0 && <span style={{ color: 'var(--yellow, #fbbf24)' }}>{t('orgBubble.needsCount', { count: summary.needs })}</span>}
            {cost > 0 && <span title={estimated ? t('bubbles.costEstimated') : undefined}>{estimated ? '≈' : ''}${cost.toFixed(4)}</span>}
          </div>
        </div>
      )}
    </div>
  )
}

// BubbleDock is the stack of coder chat bubbles (#227) at the bottom of
// the window: one bubble per open coder chat, a "+" for a new one, and a
// grip that moves the dock to the other side. Bubbles past the sixth go
// into a "+N" bubble that fans out.
export function BubbleDock({ store, onOpen, onClose, registerEl, canCreate = true, titleOf, orbitOf = null, onOpenAgent }) {
  const { t } = useTranslation()
  const [dragKey, setDragKey] = useState('')
  const [fanOpen, setFanOpen] = useState(false)
  const gripStart = useRef(null)
  const { expandedKey, side } = store
  // An agent opened from an orbit has no bubble of its own: it is reached from its parent's orbit.
  const bubbles = store.bubbles.filter(b => !b.lite)
  const { visible, overflow } = layoutBubbles(bubbles, expandedKey)

  const onDropOn = (key) => {
    if (dragKey && dragKey !== key) store.reorder(dragKey, key)
    setDragKey('')
  }
  const bubbleProps = b => ({
    bubble: b, summary: store.summaryOf(b.key), expanded: b.key === expandedKey,
    onOpen: k => { setFanOpen(false); onOpen(k) }, onClose, onDragStart: setDragKey, onDropOn,
    dragging: dragKey === b.key, registerEl, titleOf,
    orbit: orbitOf && b.conversationId ? orbitOf(b) : null, side, onOpenAgent,
  })
  if (bubbles.length === 0 && !canCreate) return null
  const overflowUnread = overflow.reduce((n, b) => n + (store.summaryOf(b.key).unread || 0), 0)

  return (
    <div className={`bubble-dock ${side}`} data-testid="bubble-dock" role="toolbar" aria-label={t('bubbles.dockLabel')}>
      {visible.map(b => <ChatBubble key={b.slot || b.key} {...bubbleProps(b)} />)}
      {overflow.length > 0 && (
        <div className="bubble-wrap">
          <button type="button" className="bubble" data-status="idle" onClick={() => setFanOpen(o => !o)}
            aria-expanded={fanOpen} aria-label={t('bubbles.more', { count: overflow.length })}>
            <span className="bubble-ring" aria-hidden="true" />+{overflow.length}
            {overflowUnread > 0 && <span className="bubble-badge" aria-hidden="true">{overflowUnread > 9 ? '9+' : overflowUnread}</span>}
          </button>
          {fanOpen && (
            <div className="bubble-overflow" data-testid="bubble-overflow">
              {overflow.map(b => <ChatBubble key={b.slot || b.key} {...bubbleProps(b)} />)}
            </div>
          )}
        </div>
      )}
      {canCreate && <button type="button" className="bubble-new" onClick={() => store.openDraft()} title={t('bubbles.newCoderChat')} aria-label={t('bubbles.newCoderChat')}>
        <Plus size={15} />
      </button>}
      <PersonItemTray />
      {bubbles.length > 0 && (
        <div className="bubble-grip" role="button" tabIndex={0} title={t('bubbles.moveDock')} aria-label={t('bubbles.moveDock')}
          onPointerDown={e => { gripStart.current = e.clientX; e.currentTarget.setPointerCapture?.(e.pointerId) }}
          onPointerUp={e => {
            if (gripStart.current == null) return
            gripStart.current = null
            store.setSide(e.clientX < window.innerWidth / 2 ? 'left' : 'right')
          }}
          onKeyDown={e => {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); store.setSide(side === 'left' ? 'right' : 'left') }
          }} />
      )}
    </div>
  )
}
