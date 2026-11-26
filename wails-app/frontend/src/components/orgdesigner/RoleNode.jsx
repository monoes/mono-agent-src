// One role card on the Org Designer canvas. Presentational — all mutation
// happens via callbacks, this component never touches app state directly.
//
// ── Prop contract ──────────────────────────────────────────────────────
//   node             canvas node shape from orgGraph.hydrate():
//                     { id, title, type, parentId, responsibilities, icon,
//                       color, x, y, rest, _flashAt, _isNew }
//                     `_flashAt` (epoch ms, optional) — set by the container
//                     when a live AI/external patch touches this role; if
//                     `Date.now() - _flashAt < 1500` a teal pulse ring plays.
//                     `_isNew` (bool, optional) — set by the container for
//                     one render pass when a role is freshly added; plays a
//                     scale/opacity enter animation.
//   isSelected       bool
//   isRoot           bool — node.parentId === null (root gets a Crown badge
//                     instead of a top reports_to handle)
//   subordinateCount number — count of direct children, computed by the
//                     parent (OrgCanvas) via buildTree() since a single node
//                     doesn't know the full tree
//   onSelect(id)
//   onDelete(id)
//   onStartEdgeDrag(id, e)   — mousedown on the top reports_to handle;
//                     the parent owns the actual drag-tracking (refs, like
//                     NodeRunner's edge-drag pattern)
//   onStartMove(id, e) — mousedown anywhere else on the card body (not a
//                     handle or the delete button); the parent owns the
//                     actual node-position drag-tracking (refs), same
//                     division of responsibility as onStartEdgeDrag
//   onDropTarget(id) — mouseup over this card while a pending reports_to
//                     edge is being dragged (i.e. "drop the edge here")
//   isDropCandidate  bool (optional) — parent tells us whether a pending
//                     edge is currently hovering this node, so we can render
//                     a hover-target highlight
//   isDropValid      bool (optional) — when isDropCandidate is true, whether
//                     dropping here is a valid (non-cycle) reparent; used to
//                     color the hover-target highlight green/red
//
// Position (node.x/node.y) is NOT applied here — the parent absolutely
// positions this component's wrapper; RoleNode only renders the NODE_W x
// NODE_H card itself so it can be reused inside a transformed layer.

//   (automation / live additions)
//   live             optional orgActivity.roleActivity() result — when set,
//                     the card is recoloured by status and shows a padlock
//                     for a pending gate, a file marker for assets, and a key
//                     badge for pending tool approvals
//   engineOffline    bool — automation roles show an "engine offline" warning
//   readOnly         bool — live view: select only, no move/reparent/delete
//
// An automation role (node.rest.kind === 'endpoint') renders with a workflow
// icon and an "automation" chip instead of an avatar and type.

import { Crown, X, Workflow, Lock, FileText, KeyRound, WifiOff } from 'lucide-react'
import { NODE_W, NODE_H } from './orgGraph'
import { iconUrl } from './roleIcons'
import { FullAccessBadge } from './fullAccess.jsx'
import { useTranslation } from 'react-i18next'

const FLASH_WINDOW_MS = 1500
export const AUTOMATION_COLOR = '#a78bfa'

export const LIVE_STATUS_COLORS = {
  working: 'var(--green-neon, #22c55e)',
  blocked: 'var(--red, #ef4444)',
  idle: 'rgba(0,180,216,0.55)',
  completed: 'var(--teal, #00f5d4)',
  offline: 'rgba(148,163,184,0.25)',
}

function typeChip(type, t) {
  return type || t('ui6OrgsDesigner.node_role')
}

export function isAutomationNode(node) {
  return node?.rest?.kind === 'endpoint'
}

export default function RoleNode({
  node,
  isSelected,
  isRoot,
  subordinateCount = 0,
  onSelect,
  onDelete,
  onStartEdgeDrag,
  onStartMove,
  onDropTarget,
  isDropCandidate = false,
  isDropValid = true,
  live = null,
  engineOffline = false,
  readOnly = false,
  // Full access (#205): the role's roles_access entry, or {} when its policy
  // declares full access but no state came back; null for a scoped role.
  fullAccess = null,
}) {
  const { t } = useTranslation()
  const isAutomation = isAutomationNode(node)
  const color = node.color || (isAutomation ? AUTOMATION_COLOR : isRoot ? 'var(--yellow)' : 'var(--cyan)')
  const isOrphan = !isRoot && node.parentId == null
  const isFlashing = node._flashAt && (Date.now() - node._flashAt < FLASH_WINDOW_MS)
  const liveStatus = live ? (live.status || 'offline') : null

  const borderColor = isDropCandidate
    ? (isDropValid ? 'var(--green)' : 'var(--red)')
    : isSelected
      ? 'var(--cyan-bright)'
      : liveStatus
        ? (live.pendingGate ? 'var(--red, #ef4444)' : LIVE_STATUS_COLORS[liveStatus] || LIVE_STATUS_COLORS.offline)
        : isAutomation
          ? `${AUTOMATION_COLOR}88`
          : isRoot
            ? 'var(--yellow)'
            : isOrphan
              ? 'var(--yellow)'
              : 'rgba(0,180,216,0.14)'

  const boxShadow = isDropCandidate
    ? `0 0 0 1.5px ${isDropValid ? 'var(--green)' : 'var(--red)'}55, 0 12px 32px rgba(0,0,0,.7)`
    : isSelected
      ? '0 0 0 1.5px rgba(0,212,255,0.4), 0 12px 32px rgba(0,0,0,.7)'
      : isRoot
        ? '0 0 0 1px rgba(234,179,8,0.35), 0 8px 24px rgba(0,0,0,.55)'
        : '0 6px 20px rgba(0,0,0,.5)'

  const classNames = [
    isFlashing ? 'od-node-ai-edit' : null,
    node._isNew ? 'od-node-enter' : null,
    liveStatus === 'working' ? 'od-live-working' : null,
  ].filter(Boolean).join(' ')

  const lastAsset = live?.assets?.length ? live.assets[live.assets.length - 1] : null

  return (
    <div
      className={classNames || undefined}
      data-live-status={liveStatus || undefined}
      data-kind={isAutomation ? 'automation' : 'agent'}
      style={{
        position: 'relative',
        width: NODE_W,
        height: NODE_H,
        background: isAutomation
          ? 'linear-gradient(160deg,#171430 0%,#0d0f22 100%)'
          : 'linear-gradient(160deg,#0d1a28 0%,#091220 100%)',
        border: `1.5px ${isOrphan ? 'dashed' : 'solid'} ${borderColor}`,
        borderRadius: isAutomation ? 4 : 10,
        boxShadow,
        opacity: liveStatus === 'offline' ? 0.7 : 1,
        userSelect: 'none',
        overflow: 'visible',
        transition: 'border-color 140ms, box-shadow 140ms, opacity 140ms',
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        padding: '0 10px',
        cursor: readOnly ? 'pointer' : 'grab',
      }}
      onMouseDown={(e) => {
        e.stopPropagation()
        onSelect?.(node.id)
        if (!readOnly) onStartMove?.(node.id, e)
      }}
      onMouseUp={() => onDropTarget?.(node.id)}
      onMouseEnter={(e) => { e.currentTarget.dataset.hover = '1' }}
      onMouseLeave={(e) => { delete e.currentTarget.dataset.hover }}
    >
      {isAutomation ? (
        <div aria-label={t('ui6OrgsDesigner.node_automation')} style={{
          width: 40, height: 40, borderRadius: 8, flexShrink: 0, display: 'flex', alignItems: 'center', justifyContent: 'center',
          background: '#120f26', border: `1px solid ${AUTOMATION_COLOR}66`,
        }}>
          <Workflow size={20} color={AUTOMATION_COLOR} />
        </div>
      ) : (<>
      {/* Avatar — always <img>, never inlined SVG (clipPath id collisions) */}
      <img
        src={iconUrl(node.icon || 'coder')}
        loading="lazy"
        alt=""
        width={40}
        height={40}
        style={{ borderRadius: '50%', flexShrink: 0, background: '#0a1420', border: `1px solid ${color}44` }}
        onError={(e) => {
          e.currentTarget.style.display = 'none'
          const fallback = e.currentTarget.nextSibling
          if (fallback) fallback.style.display = 'flex'
        }}
      />
      <div style={{
        display: 'none', width: 40, height: 40, borderRadius: '50%', flexShrink: 0,
        alignItems: 'center', justifyContent: 'center', fontSize: 18,
        background: '#0a1420', border: `1px solid ${color}44`,
      }}>
        {isRoot ? '👑' : '🙂'}
      </div>
      </>)}

      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span style={{
          fontWeight: 600, fontSize: 12.5, color: '#e2e8f0',
          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}>
          {node.title || t('ui6OrgsDesigner.node_untitled')}
        </span>
        <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          <span style={{
            alignSelf: 'flex-start',
            fontFamily: 'var(--font-mono)', fontSize: 9,
            color: isAutomation ? AUTOMATION_COLOR : 'var(--text-muted)',
            background: isAutomation ? `${AUTOMATION_COLOR}14` : 'rgba(0,180,216,0.08)',
            border: `1px solid ${isAutomation ? `${AUTOMATION_COLOR}55` : 'rgba(0,180,216,0.16)'}`,
            borderRadius: 8, padding: '1px 6px', textTransform: 'lowercase',
          }}>
            {isAutomation ? t('ui6OrgsDesigner.node_automation') : isRoot ? t('ui6OrgsDesigner.node_root') : typeChip(node.type, t)}
          </span>
          {fullAccess && <FullAccessBadge entry={fullAccess} compact />}
          {liveStatus && (
            <span data-testid="live-status" style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: LIVE_STATUS_COLORS[liveStatus] || 'var(--text-muted)' }}>
              {t(`ui6OrgsDesigner.status_${liveStatus}`, { defaultValue: liveStatus })}
            </span>
          )}
        </div>
      </div>

      {/* Live markers: pending gate (padlock), assets (file), approvals (key) */}
      {live && (live.pendingGate || live.assetCount > 0 || live.pendingApprovals > 0 || live.pendingQuestions > 0) && (
        <div style={{ position: 'absolute', top: -9, left: 8, display: 'flex', gap: 4, pointerEvents: 'auto' }}>
          {live.pendingGate && (
            <span data-testid="live-gate" title={t('ui6OrgsDesigner.gate_pending', { name: live.pendingGate.name || live.pendingGate.gateId })} style={markerStyle('var(--red, #ef4444)')}>
              <Lock size={10} />
            </span>
          )}
          {(live.pendingApprovals > 0 || live.pendingQuestions > 0) && (
            <span data-testid="live-approvals" title={t('ui6OrgsDesigner.approvals_waiting', { approvals: live.pendingApprovals, questions: live.pendingQuestions })} style={markerStyle('#eab308')}>
              <KeyRound size={9} />{live.pendingApprovals + live.pendingQuestions}
            </span>
          )}
          {live.assetCount > 0 && (
            <span data-testid="live-asset" title={lastAsset ? t('ui6OrgsDesigner.last_file', { path: lastAsset.path }) : t('ui6OrgsDesigner.files_written')} style={markerStyle('var(--cyan, #00b4d8)')}>
              <FileText size={9} />{live.assetCount}
            </span>
          )}
        </div>
      )}

      {/* Delete button — hover-revealed, matches NodeRunner's card affordance */}
      {!readOnly && <button
        onMouseDown={e => e.stopPropagation()}
        onClick={e => { e.stopPropagation(); onDelete?.(node.id) }}
        title={t('ui6OrgsDesigner.delete_role')}
        style={{
          position: 'absolute', top: -8, right: -8,
          width: 18, height: 18, borderRadius: '50%',
          background: '#151f30', border: '1px solid rgba(148,163,184,.3)',
          cursor: 'pointer', color: 'rgba(148,163,184,.7)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          opacity: 0, transition: 'opacity 100ms, color 100ms, border-color 100ms',
        }}
        className="od-node-delete-btn"
        onMouseEnter={e => { e.currentTarget.style.color = '#ef4444'; e.currentTarget.style.borderColor = '#ef4444' }}
        onMouseLeave={e => { e.currentTarget.style.color = 'rgba(148,163,184,.7)'; e.currentTarget.style.borderColor = 'rgba(148,163,184,.3)' }}
      >
        <X size={11} />
      </button>}

      {/* Top handle: Crown for root, else reports_to drag handle */}
      {isRoot ? (
        <div style={{
          position: 'absolute', top: -14, left: '50%', transform: 'translateX(-50%)',
          display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 1,
          pointerEvents: 'none',
        }}>
          <Crown size={16} color="var(--yellow)" fill="var(--yellow)" />
        </div>
      ) : (
        <div
          onMouseDown={(e) => { e.stopPropagation(); if (!readOnly) onStartEdgeDrag?.(node.id, e) }}
          title={readOnly ? undefined : t('ui6OrgsDesigner.drag_reports_to')}
          style={{
            position: 'absolute', top: -6, left: '50%', transform: 'translateX(-50%)',
            width: 12, height: 12, borderRadius: '50%',
            background: '#1e293b', border: `1.5px solid ${color}88`,
            cursor: 'crosshair',
          }}
          onMouseEnter={e => { e.currentTarget.style.background = color }}
          onMouseLeave={e => { e.currentTarget.style.background = '#1e293b' }}
        />
      )}

      {/* Bottom handle + subordinate-count badge */}
      <div style={{
        position: 'absolute', bottom: -6, left: '50%', transform: 'translateX(-50%)',
        width: 12, height: 12, borderRadius: '50%',
        background: '#1e293b', border: `1.5px solid ${color}88`,
        pointerEvents: 'none',
      }} />
      {subordinateCount > 0 && (
        <div style={{
          position: 'absolute', bottom: -18, left: '50%', transform: 'translateX(-50%)',
          fontFamily: 'var(--font-mono)', fontSize: 9, color: '#0a1420', fontWeight: 700,
          background: color, borderRadius: 8, padding: '1px 5px',
          whiteSpace: 'nowrap',
        }}>
          {subordinateCount}
        </div>
      )}

      {/* Orphan / engine-offline warnings */}
      {(isOrphan || (isAutomation && engineOffline)) && (
        <div style={{
          position: 'absolute', top: NODE_H + 4, left: 0, right: 0,
          display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 1,
          fontSize: 9.5, color: 'var(--yellow)',
          fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap', pointerEvents: 'none',
        }}>
          {isOrphan && <span>{t('ui6OrgsDesigner.no_manager')}</span>}
          {isAutomation && engineOffline && (
            <span data-testid="engine-offline" title={t('ui6OrgsDesigner.engine_offline_title')} style={{ display: 'inline-flex', alignItems: 'center', gap: 3 }}>
              <WifiOff size={9} /> {t('ui6OrgsDesigner.engine_offline')}
            </span>
          )}
        </div>
      )}

      <style>{`
        [data-hover="1"] .od-node-delete-btn { opacity: 1; }
      `}</style>
    </div>
  )
}

function markerStyle(color) {
  return {
    display: 'inline-flex', alignItems: 'center', gap: 2, height: 16, padding: '0 4px', borderRadius: 8,
    fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700,
    color, background: '#0a1420', border: `1px solid ${color}`,
  }
}
