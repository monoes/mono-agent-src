import { useCallback } from 'react'
import { notify } from '../../services/api.js'
import { CATEGORY_TYPE } from './roleIcons.js'

// Add-role flows: palette drop, quick add, and "define a new role directly".
export default function useAddRoles({ orgName, selectedId, setSelectedId, nodesRef, sec, refreshFromServer }) {
  // ── Palette → canvas: add a role ─────────────────────────────────────
  const addRoleFromArchetype = useCallback(async (item, worldX, worldY, droppedOnNodeId) => {
    if (!orgName) return
    const type = CATEGORY_TYPE[item.category] || 'specialist'
    const role = {
      title: item.label,
      type,
      reports_to: resolveDefaultParent(droppedOnNodeId),
      responsibilities: [],
      ui: { x: worldX, y: worldY, icon: item.id },
    }
    const res = await sec.createRole(role, droppedOnNodeId, { x: worldX, y: worldY })
    if (!res || res.error) { notify('add role', res?.error || 'failed to add role'); return }
    refreshFromServer(res)
  }, [orgName, refreshFromServer, sec.createRole]) // eslint-disable-line react-hooks/exhaustive-deps

  // resolveDefaultParent picks reports_to for a new role when the caller
  // didn't drop it directly onto an existing card: an explicit target wins,
  // otherwise fall back to the org's current root (never bare null unless
  // the canvas is genuinely empty) — a bare null default here is exactly
  // what produced "multiple root roles" the moment a second role was added
  // without a card selected.
  const resolveDefaultParent = useCallback((explicitParentId) => {
    if (explicitParentId) return explicitParentId
    const root = nodesRef.current.find(n => n.parentId == null)
    return root ? root.id : null
  }, [])

  const quickAddRole = useCallback(async (item) => {
    const anchor = selectedId ? nodesRef.current.find(n => n.id === selectedId) : null
    const parentId = resolveDefaultParent(anchor?.id || null)
    const parentNode = parentId ? nodesRef.current.find(n => n.id === parentId) : null
    const x = (parentNode?.x ?? 0) + 40
    const y = (parentNode ? parentNode.y + 140 : 0)
    await addRoleFromArchetype(item, x, y, parentId)
  }, [selectedId, addRoleFromArchetype, resolveDefaultParent])

  // "Define a new role directly" — a blank role not tied to any palette
  // archetype, for when the user wants to author a role from scratch
  // (title/type/responsibilities in the inspector) rather than starting
  // from an icon. Placed under the currently selected role, or the org's
  // root, or becomes the root itself if the canvas is genuinely empty.
  const addCustomRole = useCallback(async () => {
    if (!orgName) return
    const anchor = selectedId ? nodesRef.current.find(n => n.id === selectedId) : null
    const parentId = resolveDefaultParent(anchor?.id || null)
    const parentNode = parentId ? nodesRef.current.find(n => n.id === parentId) : null
    const x = (parentNode?.x ?? 0) + 40
    const y = (parentNode ? parentNode.y + 140 : 0)
    const role = {
      title: 'New Role',
      type: 'specialist',
      reports_to: parentId,
      responsibilities: [],
      ui: { x, y, icon: 'coder' },
    }
    const res = await sec.createRole(role, anchor?.id || null, null)
    if (!res || res.error) { notify('add role', res?.error || 'failed to add role'); return }
    refreshFromServer(res)
    // Select the freshly created role so the inspector opens on it right
    // away — the whole point of "define a role directly" is to land in
    // edit mode, not just drop a placeholder card on the canvas.
    if (res.org?.roles) {
      const known = new Set(nodesRef.current.map(n => n.id))
      const created = res.org.roles.find(r => r.title === 'New Role' && (sec.on ? !known.has(r.id) : r.reports_to === parentId))
      if (created) setSelectedId(created.id)
    }
  }, [orgName, selectedId, resolveDefaultParent, refreshFromServer, sec.createRole, sec.on]) // eslint-disable-line react-hooks/exhaustive-deps

  return { addRoleFromArchetype, resolveDefaultParent, quickAddRole, addCustomRole }
}
