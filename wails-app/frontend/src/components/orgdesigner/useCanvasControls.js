import { useEffect, useRef, useState } from 'react'
import { fitCamera } from './orgGraph'

const ZOOM_MIN = 0.15
const ZOOM_MAX = 2.5

// OrgCanvas's resize observer, cursor-anchored wheel zoom and keyboard shortcuts.
export default function useCanvasControls({ wrapperRef, cameraRef, nodesRef, spaceHeldRef, onCameraChange, onViewportResize, getViewport }) {
  const [measured, setMeasured] = useState({ width: 0, height: 0 })
  // ── Container resize → re-render (ResizeObserver, not window resize) ────
  const onViewportResizeRef = useRef(onViewportResize)
  onViewportResizeRef.current = onViewportResize
  useEffect(() => {
    const el = wrapperRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect || el.getBoundingClientRect()
      const next = { width: Math.round(rect.width), height: Math.round(rect.height) }
      setMeasured(prev => (prev.width === next.width && prev.height === next.height ? prev : next))
      onViewportResizeRef.current?.(next)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // ── Wheel zoom, cursor-anchored, clamped ────────────────────────────────
  useEffect(() => {
    const el = wrapperRef.current
    if (!el) return
    const onWheel = (e) => {
      e.preventDefault()
      const factor = e.deltaY < 0 ? 1.1 : 0.9
      const cam = cameraRef.current
      const z = Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, cam.zoom * factor))
      const rect = el.getBoundingClientRect()
      const mx = e.clientX - rect.left, my = e.clientY - rect.top
      onCameraChange?.({
        x: mx - (mx - cam.x) * (z / cam.zoom),
        y: my - (my - cam.y) * (z / cam.zoom),
        zoom: z,
      })
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [onCameraChange])

  // ── Keyboard: Cmd/Ctrl+0 reset, F fit, Space pan-force ──────────────────
  useEffect(() => {
    const onKeyDown = (e) => {
      if (['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName)) return
      if ((e.metaKey || e.ctrlKey) && e.key === '0') {
        e.preventDefault()
        onCameraChange?.({ x: 0, y: 0, zoom: 1 })
      } else if (e.key === 'f' || e.key === 'F') {
        const { width, height } = getViewport()
        onCameraChange?.(fitCamera(nodesRef.current, width, height))
      } else if (e.code === 'Space') {
        spaceHeldRef.current = true
      }
    }
    const onKeyUp = (e) => {
      if (e.code === 'Space') spaceHeldRef.current = false
    }
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('keyup', onKeyUp)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('keyup', onKeyUp)
    }
  }, [onCameraChange, getViewport])
  return measured
}
