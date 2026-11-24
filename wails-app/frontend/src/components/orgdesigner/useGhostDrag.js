import { useCallback, useEffect, useRef, useState } from 'react'

// Palette -> canvas ghost drag (plain mousedown-track-mouseup, see below).
export default function useGhostDrag({ camera, viewModeRef, addRoleFromArchetype, handleAutomationDrop }) {
  // ── Palette → canvas ghost drag ──────────────────────────────────────────
  // Native HTML5 drag-and-drop (wired in RolePalette.jsx/OrgCanvas.jsx) is
  // kept as a first attempt, but NodeRunner.jsx's own Workflows palette
  // deliberately does NOT rely on it — it uses a plain mousedown-track-
  // mouseup ghost drag instead, which is the proven-working pattern in this
  // exact Wails/WebKit app. Mirroring that here as the reliable path rather
  // than gambling on native DnD behaving the same way in this webview.
  const canvasOuterRef = useRef(null)
  const cameraRef = useRef(camera)
  cameraRef.current = camera
  const ghostRef = useRef(null) // { item, kind }, mirrors ghost state for the mouseup handler closure
  const [ghost, setGhost] = useState(null) // { item, kind, x, y } — screen coords; kind 'role' | 'automation'

  const isOverCanvas = useCallback((clientX, clientY) => {
    const el = canvasOuterRef.current
    if (!el) return false
    const rect = el.getBoundingClientRect()
    return clientX >= rect.left && clientX <= rect.right && clientY >= rect.top && clientY <= rect.bottom
  }, [])

  const handlePaletteDragStart = useCallback((item, e) => {
    ghostRef.current = { item, kind: 'role' }
    setGhost({ item, kind: 'role', x: e.clientX, y: e.clientY, inBounds: isOverCanvas(e.clientX, e.clientY) })
  }, [isOverCanvas])

  const handleAutomationDragStart = useCallback((item, e) => {
    ghostRef.current = { item, kind: 'automation' }
    setGhost({ item, kind: 'automation', x: e.clientX, y: e.clientY, inBounds: isOverCanvas(e.clientX, e.clientY) })
  }, [isOverCanvas])

  useEffect(() => {
    const onMove = (e) => {
      if (!ghostRef.current) return
      setGhost(g => (g ? { ...g, x: e.clientX, y: e.clientY, inBounds: isOverCanvas(e.clientX, e.clientY) } : null))
    }
    const onUp = (e) => {
      if (!ghostRef.current) return
      const { item, kind } = ghostRef.current
      ghostRef.current = null
      setGhost(null)
      if (!isOverCanvas(e.clientX, e.clientY)) return // released outside the canvas — not a drop
      if (viewModeRef.current !== 'design') return // Live and Grants views are not drop targets
      const el = canvasOuterRef.current
      const rect = el.getBoundingClientRect()
      const cam = cameraRef.current
      const wx = (e.clientX - rect.left - cam.x) / cam.zoom
      const wy = (e.clientY - rect.top - cam.y) / cam.zoom
      const target = document.elementFromPoint(e.clientX, e.clientY)
      const cardEl = target?.closest?.('[data-od-node-id]')
      const nodeId = cardEl?.dataset?.odNodeId || null
      if (kind === 'automation') handleAutomationDrop(item, wx, wy, nodeId)
      else addRoleFromArchetype(item, wx, wy, nodeId)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    return () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
  }, [addRoleFromArchetype, handleAutomationDrop, isOverCanvas])

  return { canvasOuterRef, ghost, handlePaletteDragStart, handleAutomationDragStart }
}
