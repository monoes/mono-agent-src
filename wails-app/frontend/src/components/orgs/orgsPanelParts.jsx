import { useTranslation } from 'react-i18next'
import {
  Circle, Network, Coins, GitBranch, ScrollText, ListTree, UserCheck, Gavel, Boxes,
  Inbox, MessageCircle, FileText,
} from 'lucide-react'
import { emitOpenOrgBubble } from '../../lib/appEvents.js'

// Fold button shown atop the expanded org-list panel — mirrors
// OrgDesigner.jsx's panelFoldBtnStyle for visual consistency between the
// two foldable-rail patterns in this app.
export const panelFoldBtnStyle = {
  display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4, flexShrink: 0,
  fontFamily: 'var(--font-mono)', fontSize: 9.5, padding: '5px 6px',
  background: 'transparent', border: 'none', borderBottom: '1px solid var(--border)',
  color: 'var(--text-muted)', cursor: 'pointer',
}

// Narrow vertical strip shown in place of the folded org list — click to
// expand. Mirrors OrgDesigner.jsx's panelStripStyle.
export function panelStripStyle(borderSide) {
  const base = {
    width: 26, flexShrink: 0,
    display: 'flex', alignItems: 'flex-start', justifyContent: 'center', paddingTop: 10,
    background: 'transparent', border: 'none',
    color: 'var(--text-muted)', cursor: 'pointer',
  }
  return borderSide === 'right'
    ? { ...base, borderRight: '1px solid var(--border)' }
    : { ...base, borderLeft: '1px solid var(--border)' }
}

// `needs` replaces the old Approvals tab (its auto-approve toggle is gone —
// C-42; autonomy levels resolve decisions in the daemon instead). `decisions`
// is the autonomy decision log; monomind's own decision trace moved to
// `trace`. `group` only shows for holding orgs. `queued` lists messages
// waiting for the org's next start (C-35).
export const TABS = [
  { id: 'design',     labelKey: 'orgs.tabs.design',     icon: Network },
  { id: 'overview',   labelKey: 'orgs.tabs.overview',   icon: Circle },
  { id: 'group',      labelKey: 'orgs.tabs.group',      icon: Boxes, holdingOnly: true },
  { id: 'needs',      labelKey: 'orgs.tabs.needs',      icon: UserCheck },
  { id: 'queued',     labelKey: 'orgs.tabs.queued',     icon: Inbox },
  { id: 'decisions',  labelKey: 'orgs.tabs.decisions',  icon: Gavel },
  { id: 'documents',  labelKey: 'orgs.tabs.documents',  icon: FileText },
  { id: 'logs',       labelKey: 'orgs.tabs.logs',       icon: ScrollText },
  { id: 'costs',      labelKey: 'orgs.tabs.costs',      icon: Coins },
  { id: 'flow',       labelKey: 'orgs.tabs.flow',       icon: GitBranch },
  { id: 'trace',      labelKey: 'orgs.tabs.trace',      icon: ListTree },
]

function statusColor(status) {
  if (status === 'running') return 'var(--green-neon)'
  if (status === 'crashed' || status === 'error') return 'var(--red)'
  if (status === 'paused') return '#eab308'
  return 'var(--text-muted)'
}

export function StatusDot({ status }) {
  const c = statusColor(status)
  return <span style={{ width: 6, height: 6, borderRadius: '50%', background: c, boxShadow: c !== 'var(--text-muted)' ? `0 0 5px ${c}` : 'none', flexShrink: 0 }} />
}

export function itemsOf(payload) {
  if (!payload) return []
  if (Array.isArray(payload.items)) return payload.items
  if (Array.isArray(payload)) return payload
  return []
}

// The org's actual deliverable (the post/document/answer it produced) isn't
// a distinct field anywhere — org_complete's own `summary` is often just a
// vague meta-note ("post was created and approved") rather than the verbatim
// result, and orgs rarely emit `asset` events. What IS reliable: the
// coordinator (report.outcome.by) almost always narrates the finished result
// in its own words as its last chat message right after calling
// org_complete. Pull that out so Overview can show it prominently instead of
// making people dig through the raw event log for it.
export function extractFinalMessage(events, outcome) {
  if (!outcome?.by) return null
  const list = itemsOf(events)
  const chats = list.filter(e => e.type === 'chat' && e.from === outcome.by)
  if (!chats.length) return null
  return chats[chats.length - 1].msg || null
}

// ── Detail JSON dump — the org shapes are intentionally opaque JSON, so a
// key/value table renders whatever monomind actually returned instead of
// guessing field names and silently dropping ones we didn't anticipate.
// (KVBlock itself now lives in ./KVBlock.jsx — see that file for why.) ──

export function Card({ children }) {
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 14px' }}>
      {children}
    </div>
  )
}

// Overview's promoted "what actually happened" summary — a colored outcome
// banner plus, when we could find one, the org's own words describing its
// result (see extractFinalMessage above). Renders nothing if there's no
// outcome yet (run still going, or never called org_complete).
export function OutcomeBanner({ outcome, finalMessage }) {
  if (!outcome) return null
  const color = outcome.status === 'achieved' ? 'var(--green-neon)'
    : outcome.status === 'partial' ? '#eab308'
    : outcome.status === 'failed' ? 'var(--red)'
    : 'var(--text-muted)'
  const label = outcome.status === 'achieved' ? 'Achieved'
    : outcome.status === 'partial' ? 'Partial'
    : outcome.status === 'failed' ? 'Failed'
    : outcome.status || 'Unknown'
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 6, padding: '6px 10px',
        background: 'var(--surface)', border: `1px solid ${color}`, borderRadius: 'var(--radius)',
      }}>
        <span style={{ width: 7, height: 7, borderRadius: '50%', background: color, boxShadow: `0 0 5px ${color}`, flexShrink: 0 }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color, fontWeight: 600, letterSpacing: 0.5, textTransform: 'uppercase' }}>{label}</span>
        {outcome.by && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>by {outcome.by}</span>}
      </div>
      {finalMessage && (
        <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '12px 14px', fontSize: 12.5, color: 'var(--text)', lineHeight: 1.5, whiteSpace: 'pre-wrap' }}>
          {finalMessage}
        </div>
      )}
    </div>
  )
}

// OpenAsBubbleButton opens the org as a chat bubble (#229): its live stage
// and a chat with its boss, over whatever page is open.
export function OpenAsBubbleButton({ orgName, compact = false }) {
  const { t } = useTranslation()
  return (
    <button type="button" className="btn btn-ghost btn-sm" data-testid="org-open-bubble"
      onClick={() => emitOpenOrgBubble(orgName)}
      title={t('orgBubble.openAsBubble')} aria-label={t('orgBubble.openAsBubbleNamed', { name: orgName })}
      style={compact ? { padding: '3px 5px', flexShrink: 0 } : { gap: 4, fontSize: 10 }}>
      <MessageCircle size={compact ? 11 : 12} />{!compact && ` ${t('orgBubble.openAsBubble')}`}
    </button>
  )
}
