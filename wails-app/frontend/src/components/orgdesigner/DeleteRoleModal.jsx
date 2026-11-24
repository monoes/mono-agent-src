import { useEffect, useMemo, useRef, useState } from 'react'
import { descendantsOf, buildTree } from './orgGraph.js'
import { confirm } from '../ConfirmDialog.jsx'
import i18n from '../../i18n.js'
import { useEscapeClose } from '../../lib/escapeStack.js'

/**
 * DeleteRoleModal — subordinate-handling UI for deleting a role, matching
 * the backend's Reparent ('promote' | 'reassign') vs Cascade strategies.
 *
 * When the targeted role has NO descendants, this component renders
 * nothing of its own — it delegates to the shared `confirm()` promise-based
 * dialog from ConfirmDialog.jsx for a plain yes/no, then reports back via
 * `onConfirm('promote', null)` (a no-op strategy with zero children, so
 * it's the correct default to pass through even though nothing gets
 * reparented). Only when there ARE descendants does it render a real modal
 * with the three-way strategy picker.
 *
 * @param {boolean} open
 * @param {object} node The canvas node (role) being considered for deletion.
 * @param {object[]} allNodes Full canvas node array.
 * @param {(strategy: 'promote'|'reassign'|'cascade', reassignToId: string|null) => void} onConfirm
 * @param {() => void} onClose Always called once the flow concludes (confirm or cancel).
 */
export default function DeleteRoleModal({ open, node, allNodes, onConfirm, onClose }) {
  useEscapeClose(onClose, !!open)
  const descendants = useMemo(() => (node ? descendantsOf(allNodes, node.id) : []), [node, allNodes])
  const hasDescendants = descendants.length > 0
  const firedSimpleConfirm = useRef(false)

  const [strategy, setStrategy] = useState('promote')
  const [reassignToId, setReassignToId] = useState('')
  const [cascadeConfirmText, setCascadeConfirmText] = useState('')

  // Simple case: no descendants — delegate to the shared confirm() dialog.
  useEffect(() => {
    if (!open || !node) { firedSimpleConfirm.current = false; return }
    if (hasDescendants) return
    if (firedSimpleConfirm.current) return
    firedSimpleConfirm.current = true
    ;(async () => {
      const ok = await confirm(i18n.t('orgConfirm.deleteRoleBody', { name: node.title || node.id }), {
        title: i18n.t('orgConfirm.deleteRoleTitle'),
        confirmLabel: i18n.t('orgConfirm.deleteRoleConfirm'),
        danger: true,
      })
      if (ok) onConfirm('promote', null)
      onClose()
    })()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, node, hasDescendants])

  // Reset local picker state whenever a new role (with descendants) opens.
  useEffect(() => {
    if (!open || !hasDescendants) return
    setReassignToId('')
    setCascadeConfirmText('')
  }, [open, node?.id, hasDescendants])

  if (!open || !node || !hasDescendants) return null

  const { childrenOf } = buildTree(allNodes)
  const directChildren = childrenOf.get(node.id) || []
  const isRoot = node.parentId == null
  const promoteDisabled = isRoot && directChildren.length > 1
  const grandparent = !isRoot ? (allNodes || []).find(n => n.id === node.parentId) : null

  const strategyEffective = promoteDisabled && strategy === 'promote' ? 'reassign' : strategy
  const descendantIds = new Set(descendants.map(d => d.id))
  const reassignCandidates = (allNodes || []).filter(n => n.id !== node.id && !descendantIds.has(n.id))

  const canConfirm =
    strategyEffective === 'promote' ? !promoteDisabled
    : strategyEffective === 'reassign' ? !!reassignToId
    : strategyEffective === 'cascade' ? cascadeConfirmText.trim() === node.id
    : false

  const handleConfirm = () => {
    if (!canConfirm) return
    if (strategyEffective === 'reassign') onConfirm('reassign', reassignToId)
    else onConfirm(strategyEffective, null)
    onClose()
  }

  // Indented "└─" mini-diagram of the affected subtree.
  const renderSubtree = (id, depth, isLast, prefix) => {
    const self = id === node.id ? node : descendants.find(d => d.id === id)
    const children = childrenOf.get(id) || []
    const line = depth === 0 ? '' : prefix + (isLast ? '└─ ' : '├─ ')
    const childPrefix = depth === 0 ? '' : prefix + (isLast ? '   ' : '│  ')
    return (
      <div key={id}>
        <div style={{ whiteSpace: 'pre', fontFamily: 'var(--font-mono)', fontSize: 11, color: id === node.id ? '#ef4444' : 'var(--text-secondary)' }}>
          {line}{self?.title || id}
        </div>
        {children.map((c, i) => renderSubtree(c.id, depth + 1, i === children.length - 1, childPrefix))}
      </div>
    )
  }

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label={`Delete role ${node.title || node.id}`} className="modal" style={{ width: 460 }}>
        <div className="modal-title">Delete "{node.title || node.id}"</div>

        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11.5, color: 'var(--text-secondary)', marginBottom: 12 }}>
          This role has {descendants.length} subordinate{descendants.length === 1 ? '' : 's'}. Choose what happens to them:
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 14 }}>
          {/* Promote */}
          <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', opacity: promoteDisabled ? 0.55 : 1, cursor: promoteDisabled ? 'not-allowed' : 'pointer' }}>
            <input type="radio" name="del-strategy" disabled={promoteDisabled} checked={strategyEffective === 'promote'} onChange={() => setStrategy('promote')} style={{ marginTop: 3 }} />
            <div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--text)' }}>Promote subordinates</div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)' }}>
                {promoteDisabled
                  ? 'Root role with multiple direct reports cannot be deleted this way — reassign its children individually first, or use Delete entire branch.'
                  : isRoot
                    ? `${directChildren.length} role(s) will become root roles`
                    : `${directChildren.length} role(s) will report to ${grandparent?.title || grandparent?.id || 'the parent role'} instead`}
              </div>
            </div>
          </label>

          {/* Reassign */}
          <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', cursor: 'pointer' }}>
            <input type="radio" name="del-strategy" checked={strategyEffective === 'reassign'} onChange={() => setStrategy('reassign')} style={{ marginTop: 3 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--text)' }}>Reassign to...</div>
              {strategyEffective === 'reassign' && (
                <select
                  className="form-select"
                  value={reassignToId}
                  onChange={e => setReassignToId(e.target.value)}
                  style={{ marginTop: 6 }}
                >
                  <option value="">Choose a role…</option>
                  {reassignCandidates.map(n => <option key={n.id} value={n.id}>{n.title || n.id}</option>)}
                </select>
              )}
            </div>
          </label>

          {/* Cascade */}
          <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', cursor: 'pointer' }}>
            <input type="radio" name="del-strategy" checked={strategyEffective === 'cascade'} onChange={() => setStrategy('cascade')} style={{ marginTop: 3 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: '#ef4444' }}>Delete entire branch</div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)', marginBottom: 6 }}>
                Removes {descendants.length + 1} role(s), listed below. Type the id "{node.id}" to confirm.
              </div>
              {strategyEffective === 'cascade' && (
                <input
                  className="form-input"
                  value={cascadeConfirmText}
                  onChange={e => setCascadeConfirmText(e.target.value)}
                  placeholder={node.id}
                  style={{ fontSize: 11 }}
                />
              )}
            </div>
          </label>
        </div>

        <div style={{ background: 'var(--elevated, rgba(255,255,255,0.03))', border: '1px solid var(--border-dim)', borderRadius: 6, padding: '8px 10px', maxHeight: 160, overflowY: 'auto', marginBottom: 4 }}>
          {renderSubtree(node.id, 0, true, '')}
        </div>

        <div className="modal-actions">
          <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
          <button className="btn btn-danger" onClick={handleConfirm} disabled={!canConfirm}>
            Confirm
          </button>
        </div>
      </div>
    </div>
  )
}
