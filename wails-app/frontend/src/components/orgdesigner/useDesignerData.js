import { useCallback, useEffect, useRef, useState } from 'react'
import { api, onOrgDesignUpdated } from '../../services/api.js'
import { designMeta } from './sectionsRuntimes.js'
import { hydrateWithIcons, layOutMissingPositions } from './designerHelpers.js'

// Loading, live-update application and server refresh for OrgDesigner.
export default function useDesignerData({ orgName, nodesRef, setNodes, setOrgMeta, setSelectedId }) {
  const [loading, setLoading] = useState(true)
  const [pendingUpdateCount, setPendingUpdateCount] = useState(0)
  const revRef = useRef(null)
  const isInteractingRef = useRef(false)
  const pendingPatchRef = useRef(null) // queued live-update payload while interacting

  // ── Load ──────────────────────────────────────────────────────────────
  const load = useCallback(async ({ keepSelection = false } = {}) => {
    if (!orgName) { setOrgMeta(null); setNodes([]); setLoading(false); return }
    if (!keepSelection) setLoading(true)
    const res = await api.getOrgDesign(orgName)
    if (!res || !res.org) { setOrgMeta(null); setNodes([]); setLoading(false); return }
    const { roles } = res.org
    setOrgMeta(designMeta(res))
    revRef.current = res.rev ?? null
    let hydrated = await hydrateWithIcons(roles || [])
    hydrated = layOutMissingPositions(hydrated, res.org.sections)
    setNodes(hydrated)
    if (!keepSelection) setSelectedId(null)
    setLoading(false)
  }, [orgName])

  useEffect(() => { load() }, [load])

  // ── Live updates ──────────────────────────────────────────────────────
  useEffect(() => {
    if (!orgName) return
    return onOrgDesignUpdated((payload) => {
      if (!payload || payload.orgName !== orgName) return
      if (payload.rev != null && payload.rev === revRef.current) return // echo of our own write
      if (isInteractingRef.current) {
        pendingPatchRef.current = payload
        setPendingUpdateCount(c => c + 1)
        return
      }
      applyLivePatch(payload)
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orgName])

  const applyLivePatch = useCallback(async (payload) => {
    revRef.current = payload.rev ?? revRef.current
    if (payload.deleted || !payload.org) {
      setOrgMeta(null)
      setNodes([])
      return
    }
    const { roles } = payload.org
    setOrgMeta(designMeta(payload))

    const incoming = await hydrateWithIcons(roles || [])
    const now = Date.now()
    setNodes(prev => {
      const prevById = new Map(prev.map(n => [n.id, n]))
      const next = incoming.map(n => {
        const old = prevById.get(n.id)
        if (!old) {
          // Added by someone else — lay it out under its new parent, keep a
          // fresh-enter flag for one render pass.
          return { ...n, _isNew: true, _flashAt: now }
        }
        prevById.delete(n.id)
        // Keep the user's own hand-placed position; only semantics changed.
        const changed = old.title !== n.title || old.type !== n.type || old.parentId !== n.parentId
          || JSON.stringify(old.responsibilities) !== JSON.stringify(n.responsibilities)
          || JSON.stringify(old.rest) !== JSON.stringify(n.rest) || old.icon !== n.icon
        return {
          ...n,
          x: old.x, y: old.y,
          _flashAt: changed ? now : old._flashAt,
          _isNew: false,
        }
      })
      return layOutMissingPositions(next, payload.org.sections)
    })
  }, [])

  // Drain a queued live patch once the user stops interacting.
  const endInteraction = useCallback(() => {
    isInteractingRef.current = false
    if (pendingPatchRef.current) {
      const p = pendingPatchRef.current
      pendingPatchRef.current = null
      setPendingUpdateCount(0)
      applyLivePatch(p)
    }
  }, [applyLivePatch])

  // ── Selection / persistence helpers ──────────────────────────────────
  const refreshFromServer = useCallback(async (res) => {
    if (!res) return
    if (res.rev != null) revRef.current = res.rev
    if (res.org) {
      const { roles } = res.org
      setOrgMeta(designMeta(res))
      const incoming = await hydrateWithIcons(roles || [])
      const prevById = new Map(nodesRef.current.map(n => [n.id, n]))
      const next = layOutMissingPositions(incoming.map(n => {
        const old = prevById.get(n.id)
        return old ? { ...n, x: old.x, y: old.y } : n
      }), res.org.sections)
      // Section drag/delete handlers place roles after awaiting this refresh.
      // React may batch the render until those handlers finish, so publish
      // the new semantics to the ref before their layout-only changes.
      nodesRef.current = next
      setNodes(next)
    }
  }, [])

  return { loading, pendingUpdateCount, revRef, isInteractingRef, load, refreshFromServer, endInteraction }
}
