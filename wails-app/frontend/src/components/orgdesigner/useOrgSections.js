// Sections state and actions for OrgDesigner. Every mutation is a backend
// call (internal/orgdesign enforces the rules); this hook sequences them,
// keeps the inline notice, and decides what a canvas drop means.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, notify } from '../../services/api.js'
import {
  parseSections, sectionsEnabled, sectionRects, sectionAtPoint, dropMembership,
  sectionErrors, placeNewRole, freeSectionName, freeSlotIn,
} from './sectionsGraph.js'
import i18n from '../../i18n.js'

const NOTICE_MS = 8000

export default function useOrgSections({ orgName, orgMeta, nodes, nodesRef, selectedId, setSelectedId, setInspectorOpen, refreshFromServer, onNodesChange }) {
  const list = useMemo(() => parseSections(orgMeta?.sections), [orgMeta])
  const on = sectionsEnabled(list)
  const listRef = useRef(list)
  listRef.current = list
  const [selectedName, setSelectedName] = useState(null)
  const [notice, setNotice] = useState('')
  const [prompt, setPrompt] = useState(null) // { kind: 'section' } | { kind: 'doc', from, to }, plus error
  const noticeTimer = useRef(null)
  const dragStart = useRef(null)

  useEffect(() => () => clearTimeout(noticeTimer.current), [])
  useEffect(() => { if (selectedId) setSelectedName(null) }, [selectedId])
  const say = useCallback((message) => {
    setNotice(message)
    clearTimeout(noticeTimer.current)
    noticeTimer.current = setTimeout(() => setNotice(''), NOTICE_MS)
  }, [])

  const selected = list.find(s => s.name === selectedName) || null
  const issues = useMemo(() => [...sectionErrors(nodes, list), ...(notice ? [notice] : [])], [nodes, list, notice, i18n.language])

  const selectSection = useCallback((name) => {
    setSelectedName(name)
    if (name) { setSelectedId(null); setInspectorOpen(true) }
  }, [setSelectedId, setInspectorOpen])

  // Run a backend call; on refusal show its reason inline and in a toast.
  const run = useCallback(async (op, call) => {
    const res = await call()
    if (!res || res.error) {
      const reason = res?.error || i18n.t('ui6OrgsDesigner.failed')
      say(reason)
      notify(op, reason)
      return null
    }
    await refreshFromServer(res)
    return res
  }, [refreshFromServer, say])

  // A new role in a sections org joins a section (AddOrgRoleToSection).
  const createRole = useCallback(async (role, anchorId, point) => {
    if (!on) return api.addOrgRole(orgName, role)
    const hit = point ? sectionAtPoint(sectionRects(nodesRef.current, list), point.x, point.y) : null
    const place = placeNewRole({ list, nodes: nodesRef.current, anchorId, pointRect: hit, selectedSection: selectedName })
    if (!place) return { error: i18n.t('ui6OrgsDesigner.pick_section_first') }
    // Land in a free slot of the container, not on top of a neighbour.
    const box = sectionRects(nodesRef.current, list).find(r => r.name === place.section)
    const spot = box ? freeSlotIn(nodesRef.current, box, null, role.ui && typeof role.ui.x === 'number' ? role.ui : null) : null
    return api.addOrgRoleToSection(orgName, place.section, { ...role, reports_to: place.parent, ui: { ...role.ui, ...spot } })
  }, [on, orgName, list, nodesRef, selectedName])

  // Dragging a role: remember where it started and where the containers were.
  const onDragStart = useCallback((id) => {
    const n = id && on ? nodesRef.current.find(x => x.id === id) : null
    dragStart.current = n ? { id, x: n.x, y: n.y, rects: sectionRects(nodesRef.current, list) } : null
  }, [on, list, nodesRef])

  // Dropped: inside another container moves it there, outside every one is
  // refused and snaps back.
  const onDragEnd = useCallback(async (id) => {
    const start = dragStart.current
    dragStart.current = null
    if (!start || start.id !== id) return
    const n = nodesRef.current.find(x => x.id === id)
    if (!n) return
    const verdict = dropMembership(start.rects, list, n)
    if (verdict.kind === 'stay') return
    const snapBack = () => onNodesChange(nodesRef.current.map(x => (x.id === id ? { ...x, x: start.x, y: start.y } : x)))
    if (verdict.kind === 'refuse') { snapBack(); say(verdict.reason); return }
    if (!(await run(i18n.t('ui6OrgsDesigner.op_move_role'), () => api.assignOrgRole(orgName, id, verdict.to)))) { snapBack(); return }
    // Keep the role where it was dropped only if that spot is free; else the nearest free slot.
    const box = start.rects.find(r => r.name === verdict.to)
    if (!box) return
    const spot = freeSlotIn(nodesRef.current, box, id, { x: n.x, y: n.y })
    if (spot.x !== n.x || spot.y !== n.y) onNodesChange(nodesRef.current.map(x => (x.id === id ? { ...x, ...spot } : x)))
  }, [list, nodesRef, onNodesChange, orgName, run, say])

  const submitPrompt = useCallback(async (value) => {
    const p = prompt
    const call = p.kind === 'section'
      ? () => api.addOrgSection(orgName, value, { members: selectedId ? [selectedId] : [] })
      : () => api.addOrgDocumentEdge(orgName, p.from, p.to, value)
    const res = await call()
    if (!res || res.error) { setPrompt({ ...p, error: res?.error || i18n.t('ui6OrgsDesigner.failed') }); return }
    setPrompt(null)
    await refreshFromServer(res)
    if (p.kind === 'section') selectSection(value)
  }, [prompt, orgName, selectedId, refreshFromServer, selectSection])

  return {
    list, on, selected, issues, notice,
    prompt, setPrompt, submitPrompt,
    selectSection, createRole, onDragStart, onDragEnd, say,
    openAddSection: () => setPrompt({ kind: 'section' }),
    suggestName: () => freeSectionName(list, 'section'),
    openAddEdge: (from, to) => setPrompt({ kind: 'doc', from, to }),
    removeEdge: (e) => run(i18n.t('ui6OrgsDesigner.op_remove_edge'), () => api.removeOrgDocumentEdge(orgName, e.from, e.to, e.type)),
    update: (name, patch) => run(i18n.t('ui6OrgsDesigner.op_update_section'), () => api.updateOrgSection(orgName, name, patch)),
    remove: async (name, moveTo) => {
      // Roles that move to another section get free slots in it, not their old spots.
      const gone = list.find(s => s.name === name)?.roster || []
      const box = moveTo ? sectionRects(nodesRef.current, list).find(r => r.name === moveTo) : null
      if (!(await run(i18n.t('ui6OrgsDesigner.op_delete_section'), () => api.deleteOrgSection(orgName, name, moveTo)))) return
      setSelectedName(null)
      if (!box || !gone.length) return
      let placed = nodesRef.current
      for (const id of gone) {
        const spot = freeSlotIn(placed, box, id, null)
        placed = placed.map(x => (x.id === id ? { ...x, ...spot } : x))
      }
      onNodesChange(placed)
    },
    assign: (id, section) => run(i18n.t('ui6OrgsDesigner.op_move_role'), () => api.assignOrgRole(orgName, id, section)),
  }
}
