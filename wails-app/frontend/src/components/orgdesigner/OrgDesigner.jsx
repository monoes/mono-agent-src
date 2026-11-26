// OrgDesigner — the container tying the Org Designer canvas together.
// Owns the authoritative canvas node state, all persistence calls, and the
// live-update subscription. Every child component here is a "leaf" (canvas,
// palette, inspector, icon picker, delete modal) that only reports intent
// upward via props — this file is the only place that calls the Go-bound
// api.* org-design methods or mutates role state.
//
// Backend contract (wails-app/app_orgs_design.go via src/services/api.js):
//   api.getOrgDesign(name)              -> {v, org, valid, errors}
//   api.addOrgRole(name, role)          -> {ok, rev, org} | {error}
//   api.updateOrgRole(name, id, patch)  -> {ok, rev, org} | {error}
//   api.setOrgRoleReportsTo(name,id,pid)-> {ok, rev, org} | {error}
//   api.removeOrgRole(name,id,strategy) -> {ok, rev, deletedIds} | {error}
//   api.saveOrgLayout(name, layoutMap)  -> {ok, rev}
//   onOrgDesignUpdated(cb) -> unsubscribe; payload {orgName, origin, deleted, valid, errors, org}
//
// Two backend/frontend contract gaps, bridged here rather than in the leaf
// components (documented at their call sites below):
//   1. RoleInspector's `_renameId` patch key has no backend support (no
//      role-id-rename mutator exists) — surfaced as a "not yet supported"
//      notify() rather than silently doing nothing.
//   2. DeleteRoleModal's three strategies (promote/reassign/cascade) map to
//      the backend's two (reparent/cascade) — 'reassign' is composed from
//      setOrgRoleReportsTo calls for each direct child, then a 'reparent'
//      removal (which is then a no-op reparent since no children remain).

//
// Automations (plan §7.4): the left panel also hosts the Automations drawer.
// Dropping an automation on empty canvas creates an automation role
// (AddAutomationRole); on an agent role it opens the grant dialog
// (SetOrgGrant). Grants and automation roles never go through
// UpdateOrgRole — their enforcement lives in the CLI's DB rows (C-3).
// Design / Live / Grants switches the centre between the editable canvas,
// the same canvas recoloured from the org's bus (U15), and the grants matrix.

import { sectionsOrgEnabled } from './sectionsRuntimes.js'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { GitBranch } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { tidyTree, validateStructure, buildTree, wouldCycle } from './orgGraph.js'
import { withSectionErrors, layOutSections } from './sectionsGraph.js'
import useOrgSections from './useOrgSections.js'
import OrgCanvas from './OrgCanvas.jsx'
import { useRolesAccess, withNotGranted } from './fullAccess.jsx'
import { useOrgValidateReport, mergeValidation } from './orgValidateReport.js'
import GrantsMatrix from './GrantsMatrix.jsx'
import DesignerToolbar from './DesignerToolbar.jsx'
import OrgSignatureBanner from '../orgs/OrgSignatureBanner.jsx'
import { isAutomationNode } from './RoleNode.jsx'
import useOrgAutomations from './useOrgAutomations.js'
import useOrgActivity from './useOrgActivity.js'
import useOrgBudget from './useOrgBudget.js'
import { bySection, roleBudgets } from './budgetModel.js'
import useDesignerData from './useDesignerData.js'
import useRoleMutations from './useRoleMutations.js'
import useAddRoles from './useAddRoles.js'
import useGhostDrag from './useGhostDrag.js'
import DesignerLeftPanel from './DesignerLeftPanel.jsx'
import DesignerInspectorPanel from './DesignerInspectorPanel.jsx'
import DesignerOverlays from './DesignerOverlays.jsx'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'

const EDGE_STYLE_KEY = 'od-edge-style'
const PALETTE_PANEL_OPEN_KEY = 'od-palette-panel-open'
const INSPECTOR_PANEL_OPEN_KEY = 'od-inspector-panel-open'
const LAYOUT_SAVE_DEBOUNCE_MS = 400
const FLASH_WINDOW_MS = 1500


export default function OrgDesigner({ orgName, fullscreen = false, onToggleFullscreen, onOpenWorkflow, onCreateWorkflow }) {
  const { t, i18n: { language } } = useTranslation()
  const [viewMode, setViewMode] = useState('design') // 'design' | 'live' | 'matrix'
  const [liveSource, setLiveSource] = useState('live') // 'live' or a past run id
  const [runs, setRuns] = useState([])
  const [leftTab, setLeftTab] = useState('roles') // 'roles' | 'automations'
  const [grantDialog, setGrantDialog] = useState(null) // { role, automation, grant }
  const automationData = useOrgAutomations(orgName)
  const [orgMeta, setOrgMeta] = useState(null) // { name, goal, status, schedule, run_config, ... } minus roles
  const [nodes, setNodes] = useState([])
  const [camera, setCamera] = useState({ x: 0, y: 0, zoom: 1 })
  const [selectedId, setSelectedId] = useState(null)
  const [pendingEdgeRaw, setPendingEdgeRaw] = useState(null) // { childId, sx, sy, tx, ty, hoveredNodeId }
  const [iconPickerFor, setIconPickerFor] = useState(null) // role id, or null
  const [deleteTarget, setDeleteTarget] = useState(null) // node, or null
  const [edgeStyle, setEdgeStyle] = useState(() => {
    try { return localStorage.getItem(EDGE_STYLE_KEY) === 'elbow' ? 'elbow' : 'bezier' } catch { return 'bezier' }
  })
  // Both side panels start folded — the canvas is the point of the Design
  // tab; the palette and inspector are opt-in via their toggle strips (or,
  // for the inspector, opened automatically by selecting a role below).
  const [paletteOpen, setPaletteOpenState] = useState(() => {
    try { return localStorage.getItem(PALETTE_PANEL_OPEN_KEY) === '1' } catch { return false }
  })
  const [inspectorOpen, setInspectorOpenState] = useState(() => {
    try { return localStorage.getItem(INSPECTOR_PANEL_OPEN_KEY) === '1' } catch { return false }
  })
  const setPaletteOpen = useCallback((v) => {
    setPaletteOpenState(v)
    try { localStorage.setItem(PALETTE_PANEL_OPEN_KEY, v ? '1' : '0') } catch { /* ignore */ }
  }, [])
  const setInspectorOpen = useCallback((v) => {
    setInspectorOpenState(v)
    try { localStorage.setItem(INSPECTOR_PANEL_OPEN_KEY, v ? '1' : '0') } catch { /* ignore */ }
  }, [])
  // Clicking a role opens ONLY the inspector — the palette's open/closed
  // state is left exactly as-is (manual toggle is the only way to open it).
  const handleSelectNode = useCallback((id) => {
    setSelectedId(id)
    setInspectorOpen(true)
  }, [setInspectorOpen])

  const nodesRef = useRef(nodes)
  nodesRef.current = nodes
  const viewModeRef = useRef(viewMode)
  viewModeRef.current = viewMode
  const layoutSaveTimerRef = useRef(null)
  const dirtyLayoutRef = useRef(new Map()) // roleId -> {x,y,icon,color} awaiting a debounced save


  const { loading, pendingUpdateCount, revRef, isInteractingRef, load, refreshFromServer, endInteraction } = useDesignerData({ orgName, nodesRef, setNodes, setOrgMeta, setSelectedId })

  // ── Validation (recomputed on every node change) ─────────────────────
  // The role config without positions: what full-access grants and
  // `org validate` depend on, so dragging a role doesn't refetch them.
  const configStamp = useMemo(() => JSON.stringify(nodes.map(n => [n.id, n.parentId, n.title, n.type, n.responsibilities, n.rest])), [nodes])
  // Full-access state per role (#205), and `org validate`'s report (which
  // carries full-access taint problems) merged into the structural checks.
  const rolesAccess = useRolesAccess(orgName, configStamp)
  const cliValidation = useOrgValidateReport(orgName, configStamp)
  const fullAccessByRole = useMemo(() => withNotGranted(rolesAccess.byRole, cliValidation.unacknowledged), [rolesAccess.byRole, cliValidation.unacknowledged])


  // ── Canvas node drag → debounced layout save ──────────────────────────
  const handleNodesChange = useCallback((updated) => {
    setNodes(updated)
    for (const n of updated) {
      dirtyLayoutRef.current.set(n.id, { x: n.x, y: n.y, icon: n.icon || '', color: n.color || '' })
    }
    if (layoutSaveTimerRef.current) clearTimeout(layoutSaveTimerRef.current)
    layoutSaveTimerRef.current = setTimeout(async () => {
      if (!orgName || dirtyLayoutRef.current.size === 0) return
      const layout = Object.fromEntries(dirtyLayoutRef.current)
      dirtyLayoutRef.current = new Map()
      const res = await api.saveOrgLayout(orgName, layout)
      if (res?.rev != null) revRef.current = res.rev
    }, LAYOUT_SAVE_DEBOUNCE_MS)
  }, [orgName])

  // ── Sections (useOrgSections.js): membership by drop, section edits ─────────
  const sec = useOrgSections({
    orgName, orgMeta, nodes, nodesRef, selectedId, setSelectedId, setInspectorOpen,
    refreshFromServer, onNodesChange: handleNodesChange,
  })
  const validation = useMemo(() => mergeValidation(withSectionErrors(validateStructure(nodes), nodes, sec.list), cliValidation), [nodes, cliValidation, sec.list, language])

  const { addRoleFromArchetype, resolveDefaultParent, quickAddRole, addCustomRole } = useAddRoles({ orgName, selectedId, setSelectedId, nodesRef, sec, refreshFromServer })


  // ── Automation drop: empty canvas → automation role; agent role → grant ──
  const handleAutomationDrop = useCallback(async (automation, worldX, worldY, droppedOnNodeId) => {
    if (!orgName || !automation?.alias) return
    if (droppedOnNodeId) {
      const role = nodesRef.current.find(n => n.id === droppedOnNodeId)
      if (!role) return
      if (isAutomationNode(role)) { notify(i18n.t('ui6OrgsDesigner.op_grant_automation'), i18n.t('ui6OrgsDesigner.endpoint_no_grants')); return }
      const grant = automationData.grants.find(g => g.role === role.id && g.alias === automation.alias) || null
      setGrantDialog({ role, automation, grant })
      return
    }
    const parentId = resolveDefaultParent(null)
    if (!parentId) { notify(i18n.t('ui6OrgsDesigner.op_add_automation_role'), i18n.t('ui6OrgsDesigner.boss_first')); return }
    const res = await api.addAutomationRole(orgName, {
      alias: automation.alias, reports_to: parentId, title: automation.workflow_name || automation.alias,
    })
    if (!res || res.error) { notify(i18n.t('ui6OrgsDesigner.op_add_automation_role'), res?.error || i18n.t('ui6OrgsDesigner.add_automation_role_failed')); return }
    const roleId = res.role?.id
    if (roleId) {
      const saved = await api.saveOrgLayout(orgName, { [roleId]: { x: worldX, y: worldY, icon: '', color: '' } })
      if (saved?.rev != null) revRef.current = saved.rev
    }
    await Promise.all([load({ keepSelection: true }), automationData.refresh()])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orgName, automationData.grants, automationData.refresh])

  const { canvasOuterRef, ghost, handlePaletteDragStart, handleAutomationDragStart } = useGhostDrag({ camera, viewModeRef, addRoleFromArchetype, handleAutomationDrop })


  const handleNodeDragStart = useCallback((id) => { isInteractingRef.current = true; sec.onDragStart(id) }, [sec.onDragStart]) // eslint-disable-line react-hooks/exhaustive-deps
  const handleNodeDragEnd = useCallback(async (id) => { await sec.onDragEnd(id); endInteraction() }, [endInteraction, sec.onDragEnd]) // eslint-disable-line react-hooks/exhaustive-deps

  // ── Reports-to edge drag ───────────────────────────────────────────────
  const handlePendingEdgeStart = useCallback((childId, sx, sy) => {
    isInteractingRef.current = true
    setPendingEdgeRaw({ childId, sx, sy, tx: sx, ty: sy, hoveredNodeId: null })
  }, [])
  const handlePendingEdgeChange = useCallback((childId, tx, ty, hoveredNodeId) => {
    setPendingEdgeRaw(prev => (prev ? { ...prev, tx, ty, hoveredNodeId } : prev))
  }, [])
  const pendingEdge = useMemo(() => {
    if (!pendingEdgeRaw) return null
    const { childId, hoveredNodeId } = pendingEdgeRaw
    const valid = !!hoveredNodeId && !wouldCycle(nodesRef.current, childId, hoveredNodeId)
    return { ...pendingEdgeRaw, validTargetId: valid ? hoveredNodeId : null, valid }
  }, [pendingEdgeRaw])

  const handleEdgeCommit = useCallback(async (childId, parentId) => {
    setPendingEdgeRaw(null)
    endInteraction()
    if (!orgName || childId === parentId) return
    const res = await api.setOrgRoleReportsTo(orgName, childId, parentId || '')
    if (!res || res.error) { notify(i18n.t('ui6OrgsDesigner.op_set_reports_to'), res?.error || i18n.t('ui6OrgsDesigner.failed')); if (sec.on) sec.say(res?.error || i18n.t('ui6OrgsDesigner.failed')); return }
    refreshFromServer(res)
  }, [orgName, refreshFromServer, endInteraction, sec.on, sec.say])

  const handleEdgeCycleRejected = useCallback((childId, parentId) => {
    setPendingEdgeRaw(null)
    endInteraction()
    notify(i18n.t('ui6OrgsDesigner.op_reports_to'), i18n.t('ui6OrgsDesigner.circular_reporting'))
  }, [endInteraction])

  const { handlePatch, handleSetSchedule, handleSetReportsTo, handlePromoteToRoot, handleDeleteConfirm, handleDenyBash, handleGrantSaved } = useRoleMutations({
    orgName, refreshFromServer, nodesRef, selectedId, setSelectedId, deleteTarget, setDeleteTarget,
    load, automationData, setGrantDialog,
  })

  // ── Live view: runs to replay, and the reducer feed ─────────────────────
  useEffect(() => {
    if (viewMode !== 'live' || !orgName) return
    let cancelled = false
    api.getOrgReport(orgName, true).then(res => {
      if (cancelled) return
      const items = Array.isArray(res?.items) ? res.items : []
      setRuns(items.map(r => r.run).filter(Boolean))
    })
    return () => { cancelled = true }
  }, [viewMode, orgName])
  useEffect(() => { setLiveSource('live'); setViewMode('design') }, [orgName])

  const activity = useOrgActivity({ orgName, nodes, enabled: viewMode === 'live', source: liveSource })
  const budget = useOrgBudget({ orgName, enabled: viewMode === 'live' && sec.on, run: liveSource === 'live' ? '' : liveSource, live: liveSource === 'live' })
  const engineOffline = automationData.daemonRunning === false

  // ── Icon picker ─────────────────────────────────────────────────────────
  const selectedNode = useMemo(() => nodes.find(n => n.id === selectedId) || null, [nodes, selectedId])
  const iconPickerNode = useMemo(() => nodes.find(n => n.id === iconPickerFor) || null, [nodes, iconPickerFor])

  const handleIconSelected = useCallback(async (iconId) => {
    const roleId = iconPickerFor
    setIconPickerFor(null)
    if (!roleId) return
    await handlePatch(roleId, { icon: iconId })
  }, [iconPickerFor, handlePatch])

  // ── Toolbar actions ────────────────────────────────────────────────────
  const setEdgeStylePersist = (v) => {
    setEdgeStyle(v)
    try { localStorage.setItem(EDGE_STYLE_KEY, v) } catch { /* best-effort */ }
  }
  const handleTidy = useCallback(async () => {
    if (!orgName) return
    const tidied = sec.on
      ? layOutSections(nodesRef.current.map(n => ({ ...n, x: undefined, y: undefined })), sec.list)
      : tidyTree(nodesRef.current)
    setNodes(tidied)
    const layout = Object.fromEntries(tidied.map(n => [n.id, { x: n.x, y: n.y, icon: n.icon || '', color: n.color || '' }]))
    const res = await api.saveOrgLayout(orgName, layout)
    if (res?.rev != null) revRef.current = res.rev
  }, [orgName, sec.on, sec.list])

  const subordinateCounts = useMemo(() => {
    const { childrenOf } = buildTree(nodes)
    const m = new Map()
    for (const n of nodes) m.set(n.id, (childrenOf.get(n.id) || []).length)
    return m
  }, [nodes])

  if (!orgName) {
    return (
      <div className="empty-state" style={{ flex: 1 }}>
        <div className="empty-state-icon"><GitBranch size={30} /></div>
        <div className="empty-state-title">{t('ui6OrgsDesigner.select_org')}</div>
        <div className="empty-state-desc">{t('ui6OrgsDesigner.select_org_desc')}</div>
      </div>
    )
  }

  const openAutomations = () => { setLeftTab('automations'); setPaletteOpen(true) }
  const isLive = viewMode === 'live'

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      <DesignerToolbar
        orgName={orgName}
        schedule={orgMeta?.schedule}
        sectionsOrg={sectionsOrgEnabled(orgMeta)}
        onSetSchedule={handleSetSchedule}
        validation={validation}
        pendingUpdateCount={pendingUpdateCount}
        onApplyPending={endInteraction}
        viewMode={viewMode}
        onViewMode={setViewMode}
        liveSource={liveSource}
        runs={runs}
        onLiveSource={setLiveSource}
        live={activity}
        onAddRole={addCustomRole}
        onAddSection={sec.openAddSection}
        onTidy={handleTidy}
        edgeStyle={edgeStyle}
        onToggleEdgeStyle={() => setEdgeStylePersist(edgeStyle === 'bezier' ? 'elbow' : 'bezier')}
        onReload={() => { load(); automationData.refresh() }}
        fullscreen={fullscreen}
        onToggleFullscreen={onToggleFullscreen}
        onOpenAutomations={openAutomations}
      />
      <OrgSignatureBanner orgName={orgName} refreshKey={configStamp} />

      <div style={{ flex: 1, display: 'flex', minHeight: 0 }}>
        <DesignerLeftPanel paletteOpen={paletteOpen} setPaletteOpen={setPaletteOpen} leftTab={leftTab} setLeftTab={setLeftTab} orgName={orgName} automationData={automationData} handleAutomationDragStart={handleAutomationDragStart} onOpenWorkflow={onOpenWorkflow} onCreateWorkflow={onCreateWorkflow} handlePaletteDragStart={handlePaletteDragStart} quickAddRole={quickAddRole} />

        <div ref={canvasOuterRef} style={{ flex: 1, position: 'relative', minWidth: 0, display: 'flex' }}>
          {ghost && viewMode === 'design' && (
            <div style={{
              position: 'absolute', inset: 6, zIndex: 999, borderRadius: 10,
              border: `2px dashed ${ghost.inBounds ? 'var(--teal, #00f5d4)' : 'rgba(148,163,184,0.35)'}`,
              background: ghost.inBounds ? 'rgba(0,245,212,0.05)' : 'transparent',
              pointerEvents: 'none', transition: 'border-color 80ms, background 80ms',
            }} />
          )}
          {loading ? (
            <div style={{ display: 'flex', justifyContent: 'center', padding: 24, flex: 1 }}><div className="spinner" /></div>
          ) : viewMode === 'matrix' ? (
            <GrantsMatrix
              orgName={orgName}
              nodes={nodes}
              automations={automationData.automations}
              grants={automationData.grants}
              onChanged={automationData.refresh}
              onEditGrant={(role, automation, grant) => setGrantDialog({ role, automation, grant })}
            />
          ) : (
            <OrgCanvas
              nodes={nodes}
              camera={camera}
              onCameraChange={setCamera}
              edgeStyle={edgeStyle}
              selectedId={selectedId}
              onSelectNode={handleSelectNode}
              onCanvasClick={() => { setSelectedId(null); sec.selectSection(null) }}
              onNodesChange={handleNodesChange}
              onNodeDragStart={handleNodeDragStart}
              onNodeDragEnd={handleNodeDragEnd}
              pendingEdge={pendingEdge}
              onPendingEdgeStart={handlePendingEdgeStart}
              onPendingEdgeChange={handlePendingEdgeChange}
              onEdgeCommit={handleEdgeCommit}
              onEdgeCycleRejected={handleEdgeCycleRejected}
              onRoleDropFromPalette={addRoleFromArchetype}
              onDeleteNode={(id) => setDeleteTarget(nodesRef.current.find(n => n.id === id) || null)}
              liveState={isLive ? activity.state : null}
              liveNow={activity.now}
              readOnly={isLive}
              engineOffline={engineOffline}
              onAutomationDrop={handleAutomationDrop}
              fullAccessByRole={fullAccessByRole}
              sections={sec.list}
              selectedSection={sec.selected?.name || null}
              onSelectSection={sec.selectSection}
              onAddDocEdge={sec.openAddEdge}
              onRemoveDocEdge={sec.removeEdge}
              sectionIssues={sec.issues}
              sectionBudgets={isLive ? bySection(budget.report) : {}}
              roleCaps={isLive ? roleBudgets(budget.report) : {}}
              staleRates={isLive ? budget.estimate?.stale_rates || '' : ''}
            />
          )}
        </div>

        <DesignerInspectorPanel inspectorOpen={inspectorOpen} setInspectorOpen={setInspectorOpen} sec={sec} isLive={isLive} budget={budget} nodes={nodes} handlePatch={handlePatch} selectedNode={selectedNode} selectedId={selectedId} handleSetReportsTo={handleSetReportsTo} handlePromoteToRoot={handlePromoteToRoot} setIconPickerFor={setIconPickerFor} setDeleteTarget={setDeleteTarget} orgName={orgName} automationData={automationData} engineOffline={engineOffline} onOpenWorkflow={onOpenWorkflow} setGrantDialog={setGrantDialog} fullAccessByRole={fullAccessByRole} orgMeta={orgMeta} rolesAccess={rolesAccess} cliValidation={cliValidation} />
      </div>

      <DesignerOverlays sec={sec} iconPickerFor={iconPickerFor} iconPickerNode={iconPickerNode} handleIconSelected={handleIconSelected} setIconPickerFor={setIconPickerFor} deleteTarget={deleteTarget} setDeleteTarget={setDeleteTarget} nodes={nodes} handleDeleteConfirm={handleDeleteConfirm} grantDialog={grantDialog} setGrantDialog={setGrantDialog} orgName={orgName} automationData={automationData} handleGrantSaved={handleGrantSaved} handleDenyBash={handleDenyBash} ghost={ghost} />
    </div>
  )
}
