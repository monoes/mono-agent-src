// Group view for a holding org (plan §7.4, Phase 6): the holding org and its
// child orgs as cards with status, cost roll-up, group start/stop, and live
// cross-org message arcs drawn between cards (deduped, C-43).
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Play, Square, RefreshCw, Boxes, ExternalLink } from 'lucide-react'
import { api, notify, onOrgEvent } from '../../services/api.js'
import { acquireOrgEvents } from '../../lib/orgEventStreams.js'
import { confirm } from '../ConfirmDialog.jsx'
import i18n from '../../i18n.js'
import { initialGroupTraffic, applyGroupEvent, recentArcs, arcPath, ARC_RECENT_MS } from './groupTraffic.js'
import { Card, Chip, mono, mutedText, sectionLabel, smallBtn } from './ui.jsx'

const POLL_MS = 10_000

function statusColor(status) {
  if (status === 'running') return 'var(--green-neon)'
  if (status === 'crashed' || status === 'error') return 'var(--red)'
  if (status === 'paused') return '#eab308'
  return 'var(--text-muted)'
}

function usd(n) {
  const v = Number(n) || 0
  return v < 1 ? `$${v.toFixed(3)}` : `$${v.toFixed(2)}`
}

function OrgCard({ name, status, cost, liveCost, extra, isHolding, cardRef, onOpen }) {
  const color = statusColor(status)
  return (
    <div ref={cardRef} data-group-org={name} style={{ position: 'relative', zIndex: 1, width: 200 }}>
      <Card style={{ padding: '10px 12px', borderColor: isHolding ? 'rgba(234,179,8,0.45)' : undefined }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <span style={{ width: 7, height: 7, borderRadius: '50%', background: color, boxShadow: status === 'running' ? `0 0 5px ${color}` : 'none', flexShrink: 0 }} />
          <span style={{ ...mono, fontSize: 12, fontWeight: 600, color: 'var(--text)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</span>
          {onOpen && (
            <button onClick={() => onOpen(name)} title={`Open ${name}`} aria-label={`Open ${name}`} style={{ ...smallBtn, border: 'none', padding: 2 }}>
              <ExternalLink size={11} />
            </button>
          )}
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 6, alignItems: 'center' }}>
          <span style={{ ...mutedText, color }}>{status || 'unknown'}</span>
          {isHolding && <Chip color="#eab308">holding</Chip>}
          {extra}
        </div>
        <div style={{ ...mutedText, marginTop: 6 }}>
          {cost != null ? `spent ${usd(cost)}` : 'no spend yet'}
          {liveCost > 0 && <span title="From live usage events"> · live {usd(liveCost)}</span>}
        </div>
      </Card>
    </div>
  )
}

export default function GroupView({ holding, onOpenOrg }) {
  const [group, setGroup] = useState(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [traffic, setTraffic] = useState(() => initialGroupTraffic([holding]))
  const [now, setNow] = useState(() => Date.now())
  const [centers, setCenters] = useState({})
  const containerRef = useRef(null)
  const cardRefs = useRef({})

  const children = Array.isArray(group?.children) ? group.children : []
  const members = [holding, ...children.map(c => c.org)]
  const membersKey = members.join('\u0000')

  const load = useCallback(async () => {
    const res = await api.orgGroupStatus(holding)
    setLoading(false)
    if (!res || res.error) {
      setError(res?.error || 'Could not read the group status.')
      return
    }
    setError('')
    setGroup(res)
  }, [holding])

  useEffect(() => {
    setLoading(true)
    setGroup(null)
    load()
    const iv = setInterval(load, POLL_MS)
    return () => clearInterval(iv)
  }, [load])

  // Live traffic: tail every child org's bus (the holding org's own tail is
  // already open in OrgsPanel) and fold all of them through one reducer.
  useEffect(() => {
    const names = membersKey.split('\u0000').filter(Boolean)
    setTraffic(initialGroupTraffic(names))
    const childNames = names.filter(n => n !== holding)
    const releases = childNames.map(n => acquireOrgEvents(n))
    const off = onOrgEvent((payload) => {
      if (!payload?.orgName || !names.includes(payload.orgName)) return
      setTraffic(prev => applyGroupEvent(prev, payload.event, payload.orgName))
    })
    const tick = setInterval(() => setNow(Date.now()), 1000)
    return () => {
      off()
      clearInterval(tick)
      releases.forEach(release => release())
    }
  }, [membersKey, holding])

  // Card centres for the arc layer, re-measured whenever the container
  // resizes (ResizeObserver, not window resize — U15's viewport lesson).
  const measure = useCallback(() => {
    const box = containerRef.current?.getBoundingClientRect()
    if (!box) return
    const next = {}
    for (const [name, el] of Object.entries(cardRefs.current)) {
      if (!el) continue
      const r = el.getBoundingClientRect()
      next[name] = { x: r.left - box.left + r.width / 2, y: r.top - box.top + r.height / 2 }
    }
    setCenters(next)
  }, [])

  useLayoutEffect(() => { measure() }, [measure, membersKey, group])

  useEffect(() => {
    const el = containerRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => measure())
    ro.observe(el)
    return () => ro.disconnect()
  }, [measure, group])

  const run = async (verb) => {
    if (verb === 'stop') {
      const ok = await confirm(i18n.t('orgConfirm.stopGroupBody', { holding }), { title: i18n.t('orgConfirm.stopGroupTitle'), confirmLabel: i18n.t('orgConfirm.stopGroupConfirm'), danger: true })
      if (!ok) return
    }
    setBusy(true)
    try {
      const res = verb === 'start' ? await api.startOrgGroup(holding) : await api.stopOrgGroup(holding)
      if (!res || res.error) notify(`${verb} group`, res?.error || `${verb} failed`)
      else setGroup(g => (res.children ? res : g))
      await load()
    } finally {
      setBusy(false)
    }
  }

  const arcs = recentArcs(traffic, now, ARC_RECENT_MS)
  const totals = Object.values(traffic.arcs).reduce((n, a) => n + a.count, 0)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <Boxes size={12} style={{ color: 'var(--text-muted)' }} />
        <span style={sectionLabel}>Group</span>
        {group && <span style={mutedText}>roll-up {usd(group.rollup_usd)}</span>}
        {totals > 0 && <span style={mutedText}>· {totals} cross-org message{totals === 1 ? '' : 's'}</span>}
        <span style={{ flex: 1 }} />
        <button style={smallBtn} disabled={busy} onClick={() => run('start')}><Play size={10} /> Start group</button>
        <button style={smallBtn} disabled={busy} onClick={() => run('stop')}><Square size={10} /> Stop group</button>
        <button style={smallBtn} onClick={() => { setLoading(true); load() }} aria-label="Refresh group"><RefreshCw size={10} /></button>
      </div>

      {loading && !group && <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>}
      {error && <div style={{ ...mono, fontSize: 11, color: '#f87171' }}>{error}</div>}
      {group && children.length === 0 && <div style={{ ...mono, fontSize: 11, color: 'var(--text-muted)' }}>This holding org has no child orgs yet.</div>}

      {group && (
        <div ref={containerRef} data-testid="group-canvas" style={{ position: 'relative', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 36, padding: '12px 4px' }}>
          <svg aria-hidden="true" style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', pointerEvents: 'none', overflow: 'visible', zIndex: 2 }}>
            <defs>
              <marker id="group-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
                <path d="M0,0 L10,5 L0,10 z" fill="var(--cyan, #00b4d8)" />
              </marker>
            </defs>
            {arcs.map(a => {
              const p = centers[a.from]
              const q = centers[a.to]
              if (!p || !q) return null
              const age = Math.max(0, now - a.lastTs)
              const opacity = Math.max(0.2, 1 - age / ARC_RECENT_MS)
              return (
                <g key={`${a.from}->${a.to}`} data-testid="group-arc" data-from={a.from} data-to={a.to} data-count={a.count}>
                  <path d={arcPath(p.x, p.y, q.x, q.y)} stroke="var(--cyan, #00b4d8)" strokeWidth={1.8} fill="none"
                    strokeOpacity={opacity} strokeDasharray="6 5" markerEnd="url(#group-arrow)" className="od-live-edge" />
                  <title>{`${a.from} → ${a.to}: ${a.count} message${a.count === 1 ? '' : 's'}${a.subject ? ` (last: ${a.subject})` : ''}`}</title>
                </g>
              )
            })}
          </svg>
          <OrgCard
            name={holding}
            isHolding
            status={traffic.orgs[holding]?.status !== 'unknown' ? traffic.orgs[holding]?.status : undefined}
            cost={null}
            liveCost={traffic.orgs[holding]?.costUsd || 0}
            cardRef={el => { cardRefs.current[holding] = el }}
          />
          <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'center', gap: 28 }}>
            {children.map(c => (
              <OrgCard
                key={c.org}
                name={c.org}
                status={c.status}
                cost={c.cost_usd}
                liveCost={traffic.orgs[c.org]?.costUsd || 0}
                onOpen={onOpenOrg}
                cardRef={el => { cardRefs.current[c.org] = el }}
                extra={(
                  <>
                    {c.start && <Chip title="When this child starts">{c.start.replace('_', ' ')}</Chip>}
                    {c.budget_share != null && <Chip title="Share of the holding budget">{Math.round(Number(c.budget_share) * 100)}%</Chip>}
                  </>
                )}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
