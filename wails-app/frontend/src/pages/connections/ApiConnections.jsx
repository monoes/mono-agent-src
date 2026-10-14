// "API Connections" section of the Connections page (spec §7.1): the
// connection registry's platforms that have at least one non-browser auth
// method, grouped by category. A site with both an API method and a browser
// automation (e.g. Product Hunt) shows here and in Browser Automations —
// they are different things with different nodes.
import { useState } from 'react'
import { SectionHeader } from './ui.jsx'

const CATEGORY_ORDER = ['social', 'service', 'communication', 'database', 'infrastructure', 'ai', 'app', 'custom']
const CATEGORY_LABELS = { social: 'Social', service: 'Services & APIs', communication: 'Communication', database: 'Databases', infrastructure: 'Infrastructure', ai: 'AI', app: 'Apps', custom: 'Custom' }

// apiPlatforms keeps platforms that can be connected without the browser.
export function apiPlatforms(platforms) {
  return (platforms || []).filter(p => (p.methods || []).some(m => m !== 'browser'))
}

// resolveConn finds the saved API connection for a platform, if any.
export function resolveConn(platform, connections) {
  const pid = (platform.id || '').toLowerCase()
  const c = (connections || []).find(x => (x.Platform || x.platform || '').toLowerCase() === pid)
  if (!c) return null
  return { id: c.ID || c.id, account: c.Label || c.AccountID || '—', method: c.Method || c.method || '—', status: c.Status || c.status || 'active', lastTested: c.LastTested || c.last_tested }
}

function Tile({ platform, conn, onClick }) {
  const [hov, setHov] = useState(false)
  const connected = conn && conn.status === 'active'
  const expired = conn && conn.status !== 'active'
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`${platform.name}${connected ? ' (connected)' : expired ? ' (expired)' : ''}`}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        background: connected ? 'linear-gradient(145deg,var(--elevated),var(--surface))' : 'var(--surface)',
        border: connected ? '1px solid var(--border-active)' : hov ? '1px solid var(--border-bright)' : '1px solid var(--border)',
        borderRadius: 'var(--radius-lg)',
        padding: '18px 10px 12px',
        cursor: 'pointer',
        display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6,
        transition: 'all var(--transition)',
        boxShadow: connected ? 'var(--shadow-glow)' : 'none',
        userSelect: 'none',
        minWidth: 0,
      }}
    >
      <span style={{ fontSize: 24, lineHeight: 1, filter: (connected || expired) ? 'none' : 'grayscale(40%) opacity(0.7)' }}>
        {platform.iconEmoji || '🔌'}
      </span>
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 600, color: (connected || expired) ? 'var(--text)' : 'var(--text-secondary)', textAlign: 'center', lineHeight: 1.3 }}>
        {platform.name}
      </span>
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <span style={{ width: 6, height: 6, borderRadius: '50%', background: connected ? 'var(--green-neon)' : expired ? '#fbbf24' : 'var(--text-muted)', boxShadow: connected ? '0 0 5px var(--green-neon)' : expired ? '0 0 5px #fbbf24' : 'none', flexShrink: 0 }} />
        {(connected || expired) && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', maxWidth: 72, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{conn.account}</span>}
      </div>
    </button>
  )
}

// matchesQuery: a platform whose name or id contains every word of q.
export function matchesQuery(platform, q) {
  const hay = `${platform.name || ''} ${platform.id || ''}`.toLowerCase()
  return q.toLowerCase().split(/\s+/).filter(Boolean).every(w => hay.includes(w))
}

export default function ApiConnections({ platforms, connections, onSelect }) {
  const [query, setQuery] = useState('')
  const list = apiPlatforms(platforms)
  const groups = {}
  for (const p of list) {
    if (!matchesQuery(p, query)) continue
    const cat = (p.category || 'service').toLowerCase()
    if (!groups[cat]) groups[cat] = []
    groups[cat].push(p)
  }
  // The registry's order is not stable between calls; sort for a steady grid.
  for (const cat of Object.keys(groups)) groups[cat].sort((x, y) => (x.name || x.id).localeCompare(y.name || y.id))
  const total = list.filter(p => resolveConn(p, connections)).length
  const cats = [...CATEGORY_ORDER, ...Object.keys(groups).filter(c => !CATEGORY_ORDER.includes(c))]
    .filter(cat => groups[cat] && groups[cat].length > 0)

  return (
    <section aria-label="API connections">
      <SectionHeader title="API Connections" hint="OAuth, API keys, connection strings" count={`${total} / ${list.length} connected`} />
      <input type="search" value={query} onChange={e => setQuery(e.target.value)} placeholder="Filter platforms…" aria-label="Filter API platforms"
        style={{ width: '100%', maxWidth: 320, marginBottom: 14, fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 10px', outline: 'none', boxSizing: 'border-box' }} />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 22 }}>
        {cats.map(cat => {
          const items = groups[cat]
          const catConn = items.filter(p => resolveConn(p, connections)).length
          return (
            <div key={cat}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: 2 }}>
                  {CATEGORY_LABELS[cat] || cat}
                </span>
                <div style={{ flex: 1, height: 1, background: 'var(--border-dim)' }} />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)' }}>{catConn}/{items.length}</span>
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(100px, 1fr))', gap: 8 }}>
                {items.map(p => (
                  <Tile key={p.id} platform={p} conn={resolveConn(p, connections)} onClick={() => onSelect(p)} />
                ))}
              </div>
            </div>
          )
        })}
      </div>
    </section>
  )
}
