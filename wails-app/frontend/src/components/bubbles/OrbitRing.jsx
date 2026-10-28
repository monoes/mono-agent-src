import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, X, Hourglass, Pause, Minus, Activity } from 'lucide-react'
import { monogram } from '../../lib/coderBubbles.js'
import { ringPositions, linkPath, cycleIndex } from '../../lib/sessionOrbit.js'
import './orbit.css'

const STATE_ICON = { running: Activity, waiting: Hourglass, done: Check, failed: X, paused: Pause, idle: Minus }

// Sizes (px) per size step and the geometry of each variant. "arc" orbits a dock
// bubble on the side that faces the window; "ring" orbits the disc in a pane header.
const VARIANTS = {
  // gapDeg is the angle between neighbours, so a few circles sit close together and many fan out up to maxArc.
  arc: { radius: 78, gapDeg: 26, maxArc: 200, sizes: [24, 28, 32], chip: 28 },
  ring: { radius: 54, arc: 360, sizes: [28, 34, 40], chip: 30 },
}

export function statusLine(t, c) {
  const parts = [t(`bubbles.orbit.state.${c.state}`)]
  if (c.tasksTotal > 0) parts.push(t('bubbles.orbit.tasks', { done: c.tasksDone, total: c.tasksTotal }))
  if (c.spent > 0) parts.push(`$${c.spent.toFixed(2)}`)
  return parts.join(' · ')
}

function Satellite({ c, pos, size, tabIndex, onOpen, btnRef, onKeyDown, index }) {
  const { t } = useTranslation()
  const Icon = STATE_ICON[c.state] || Minus
  const label = t('bubbles.orbit.satelliteLabel', { title: c.title, state: t(`bubbles.orbit.state.${c.state}`) }) +
    (c.childCount > 0 ? `, ${t('bubbles.orbit.ownAgents', { count: c.childCount })}` : '')
  return (
    <div className="orbit-slot" style={{ '--x': pos.x, '--y': pos.y, '--d': `${index * 40}ms` }}>
      <button type="button" ref={btnRef} className={`orbit-sat s${c.size}`} data-state={c.state} data-level={c.level}
        style={{ '--size': `${size}px` }} tabIndex={tabIndex} aria-label={label} onClick={() => onOpen?.(c)} onKeyDown={onKeyDown}>
        <span className="orbit-face" aria-hidden="true">
          <span className="orbit-pulse" />
          <span className="orbit-mono">{monogram(c.title)}</span>
        </span>
        <span className="orbit-mark" aria-hidden="true"><Icon size={9} strokeWidth={3} /></span>
        {c.childCount > 0 && <span className="orbit-count" aria-hidden="true">{c.childCount > 9 ? '9+' : c.childCount}</span>}
        <span className={`orbit-tip${pos.y < -10 ? ' below' : ''}`} aria-hidden="true">
          <span className="orbit-tip-title">{c.title}</span>
          <span>{statusLine(t, c)}</span>
        </span>
      </button>
    </div>
  )
}

// OrbitRing draws a session's direct agents as small circles around a centre
// (children: the root bubble or the pane's disc), joined to it by thin curved
// links under it. orbit is sessionOrbit.orbitOf's result; onOpen(circle) opens
// that agent's chat. The circles are buttons in one ring: Left/Right (and
// Up/Down) move between them, Enter opens. Past MAX_SATELLITES the rest wait
// behind a "+N" chip that opens a list.
export function OrbitRing({ orbit, variant = 'arc', facing = 180, onOpen, label, children }) {
  const { t } = useTranslation()
  const cfg = VARIANTS[variant] || VARIANTS.arc
  const sats = orbit?.satellites || []
  const over = orbit?.overflow || []
  const slots = sats.length + (over.length ? 1 : 0)
  const [active, setActive] = useState(0)
  const [moreOpen, setMoreOpen] = useState(false)
  const refs = useRef([])
  const moreRef = useRef(null)
  const arc = cfg.arc ?? Math.min(cfg.maxArc, Math.max(0, (slots - 1) * cfg.gapDeg))
  const pos = ringPositions(slots, { radius: cfg.radius, arc: arc || 1, facing, topDown: cfg.arc === undefined })

  useEffect(() => { if (active >= slots) setActive(Math.max(0, slots - 1)) }, [active, slots])
  if (slots === 0) return children || null

  const focusAt = (i) => {
    const n = cycleIndex(i, 0, slots)
    setActive(n)
    ;(n < sats.length ? refs.current[n] : moreRef.current)?.focus()
  }
  const onKeyDown = (i) => (e) => {
    const delta = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!delta) return
    e.preventDefault()
    focusAt(cycleIndex(i, delta, slots))
  }
  const chipIndex = sats.length

  return (
    <div className={`orbit orbit-${variant}${facing === 0 ? ' face-right' : ''}`} data-testid="orbit">
      {children}
      <div className="orbit-layer" role="group" aria-label={label || t('bubbles.orbit.groupLabel')}>
        <svg className="orbit-links" width="1" height="1" aria-hidden="true" focusable="false">
          {pos.map((p, i) => <path key={i} d={linkPath(p.x, p.y)} className={`orbit-link${i < sats.length ? ` ${sats[i].state}` : ''}`} />)}
        </svg>
        {sats.map((c, i) => (
          <Satellite key={c.id} c={c} pos={pos[i]} size={cfg.sizes[c.size - 1] || cfg.sizes[0]} index={i}
            tabIndex={i === active ? 0 : -1} btnRef={el => { refs.current[i] = el }} onOpen={onOpen} onKeyDown={onKeyDown(i)} />
        ))}
        {over.length > 0 && (
          <div className="orbit-slot" style={{ '--x': pos[chipIndex].x, '--y': pos[chipIndex].y, '--d': `${chipIndex * 40}ms` }}>
            <button type="button" ref={moreRef} className="orbit-chip" style={{ '--size': `${cfg.chip}px` }} tabIndex={active === chipIndex ? 0 : -1}
              aria-expanded={moreOpen} aria-haspopup="true" aria-label={t('bubbles.orbit.more', { count: over.length })}
              onClick={() => setMoreOpen(o => !o)} onKeyDown={onKeyDown(chipIndex)}>
              +{over.length}
            </button>
            {moreOpen && (
              <MoreList items={over} onClose={() => { setMoreOpen(false); moreRef.current?.focus() }}
                onOpen={c => { setMoreOpen(false); onOpen?.(c) }} />
            )}
          </div>
        )}
      </div>
    </div>
  )
}

// MoreList is the agents past the eighth. Escape closes it and nothing else.
function MoreList({ items, onClose, onOpen }) {
  const { t } = useTranslation()
  const first = useRef(null)
  useEffect(() => { first.current?.focus() }, [])
  return (
    <ul className="orbit-more" aria-label={t('bubbles.orbit.moreTitle')}
      onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); e.preventDefault(); onClose() } }}>
      {items.map((c, i) => {
        const Icon = STATE_ICON[c.state] || Minus
        return (
          <li key={c.id}>
            <button type="button" ref={i === 0 ? first : null} data-state={c.state} onClick={() => onOpen(c)}
              aria-label={t('bubbles.orbit.satelliteLabel', { title: c.title, state: t(`bubbles.orbit.state.${c.state}`) })}>
              <Icon size={10} strokeWidth={3} aria-hidden="true" />
              <span className="orbit-more-title">{c.title}</span>
              <span className="orbit-more-state">{statusLine(t, c)}</span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}
