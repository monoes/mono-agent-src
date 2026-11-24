import { useCallback } from 'react'
import { api, notify } from '../../services/api.js'
import { isAutomationNode } from './RoleNode.jsx'

// Role mutations the inspector, canvas and grant dialog call into.
export default function useRoleMutations({
  orgName, refreshFromServer, nodesRef, selectedId, setSelectedId, deleteTarget, setDeleteTarget,
  load, automationData, setGrantDialog,
}) {
  // ── Inspector patch ────────────────────────────────────────────────────
  const handlePatch = useCallback(async (roleId, patch) => {
    if (!orgName) return
    if (Object.prototype.hasOwnProperty.call(patch, '_renameId')) {
      notify('rename role', 'Renaming a role id is not supported yet — delete and recreate the role instead.')
      const rest = { ...patch }
      delete rest._renameId
      if (Object.keys(rest).length === 0) return
      patch = rest
    }
    const res = await api.updateOrgRole(orgName, roleId, patch)
    if (!res || res.error) { notify('update role', res?.error || 'failed to update role'); return }
    refreshFromServer(res)
  }, [orgName, refreshFromServer])

  const handleSetSchedule = useCallback(async (schedule) => {
    const res = await api.setOrgSchedule(orgName, schedule)
    if (res && !res.error) refreshFromServer(res)
    return res
  }, [orgName, refreshFromServer])

  // Reports-to is NOT one of UpdateOrgRole's patchable fields (see the Go
  // side's `raw` struct in app_orgs_design.go) — it has its own dedicated
  // mutator (SetOrgRoleReportsTo) because moving a role in the tree needs
  // its own cycle check, distinct from a plain field patch. The canvas's
  // edge-drag already calls this; the inspector's "Reports to" dropdown
  // needs the same path rather than routing through handlePatch, which
  // would just silently drop the field.
  const handleSetReportsTo = useCallback(async (roleId, parentId) => {
    if (!orgName || !roleId) return
    const res = await api.setOrgRoleReportsTo(orgName, roleId, parentId || '')
    if (!res || res.error) { notify('set reports to', res?.error || 'failed to update reports-to'); return }
    refreshFromServer(res)
  }, [orgName, refreshFromServer])

  // "Set as org boss" — distinct from handleSetReportsTo(id, null): that
  // path (SetOrgRoleReportsTo) just refuses when a different root already
  // exists, so making someone else the boss needs the dedicated swap
  // mutator (PromoteToRoot) instead, correct at any depth.
  const handlePromoteToRoot = useCallback(async (roleId) => {
    if (!orgName || !roleId) return
    const res = await api.promoteRoleToRoot(orgName, roleId)
    if (!res || res.error) { notify('set as org boss', res?.error || 'failed to promote role'); return }
    refreshFromServer(res)
  }, [orgName, refreshFromServer])

  // ── Delete flow ─────────────────────────────────────────────────────────
  const handleDeleteConfirm = useCallback(async (strategy, reassignToId) => {
    const node = deleteTarget
    setDeleteTarget(null)
    if (!orgName || !node) return
    // Automation roles also own an endpoint row in the CLI's DB — remove them
    // through the CLI so the endpoint URL is revoked with the role.
    if (isAutomationNode(node)) {
      const res = await api.removeAutomationRole(orgName, node.id)
      if (!res || res.error) { notify('delete automation role', res?.error || 'failed to remove automation role'); return }
      if (selectedId === node.id) setSelectedId(null)
      await Promise.all([load({ keepSelection: true }), automationData.refresh()])
      return
    }
    if (strategy === 'reassign' && reassignToId) {
      const children = nodesRef.current.filter(n => n.parentId === node.id)
      for (const c of children) {
        const r = await api.setOrgRoleReportsTo(orgName, c.id, reassignToId)
        if (!r || r.error) { notify('reassign role', r?.error || 'failed'); return }
      }
      const res = await api.removeOrgRole(orgName, node.id, 'reparent')
      if (!res || res.error) { notify('delete role', res?.error || 'failed'); return }
      refreshFromServer(res)
      return
    }
    const backendStrategy = strategy === 'cascade' ? 'cascade' : 'reparent'
    const res = await api.removeOrgRole(orgName, node.id, backendStrategy)
    if (!res || res.error) { notify('delete role', res?.error || 'failed to delete role'); return }
    if (selectedId === node.id) setSelectedId(null)
    refreshFromServer(res)
  }, [orgName, deleteTarget, selectedId, refreshFromServer, load, automationData])

  // Grant dialog "Deny Bash" (plan §8 layer 8) — a policy patch, which
  // UpdateOrgRole does accept; the grant itself went through the CLI.
  const handleDenyBash = useCallback((role) => {
    const current = nodesRef.current.find(n => n.id === role.id) || role
    const policy = current.rest?.policy || {}
    const deny = policy.denyTools || []
    if (deny.includes('Bash')) return
    handlePatch(role.id, { policy: { ...policy, denyTools: [...deny, 'Bash'] } })
  }, [handlePatch])

  const handleGrantSaved = useCallback(async () => {
    setGrantDialog(null)
    await automationData.refresh()
  }, [automationData])

  return { handlePatch, handleSetSchedule, handleSetReportsTo, handlePromoteToRoot, handleDeleteConfirm, handleDenyBash, handleGrantSaved }
}
