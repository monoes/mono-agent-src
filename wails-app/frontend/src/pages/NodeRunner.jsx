import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import {
  Play, Square, RotateCcw, ZoomIn, ZoomOut, Trash2, Search,
  ChevronDown, ChevronRight, X, Settings2, Copy, RefreshCw,
  AlertCircle, CheckCircle, Clock, Loader, Plus,
  Save, FolderOpen, ToggleLeft, ToggleRight, List, LayoutTemplate,
  Braces, LayoutDashboard, Upload, UploadCloud,
} from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { api, notify, reportRunFailure, subscribeEvent } from '../services/api.js'
import { confirm } from '../components/ConfirmDialog.jsx'
import ResourcePickerField from '../components/ResourcePickerField.jsx'
import ImagePickerModal from '../components/ImagePickerModal'
import {
  NODE_CONFIG_FIELDS, BROWSER_NODE_GENERIC,
  deriveInputs, deriveOutputs, portsDependOnConfig, isCaseListSettled, remapSourceEdges,
  arrayFieldItems, arrayTagLabel, normalizeConfigForSave,
  canonicalOutputHandle, loopBackEdges, hasForbiddenCycle, loopEdgePath, chooseLoopEdgeY, edgePath, edgePoints,
  makeIdGen,
} from './nodeConfigFields.js'
import { SaveModal, WorkflowsModal, TriggerInputModal } from './NodeRunnerModals.jsx'
import WorkflowImportDialog from './WorkflowImportDialog.jsx'
import PublishToMonoesDialog from '../components/library/PublishToMonoesDialog.jsx'
import { derivePlatformId, isSessionPicker, isMediaField, automationIdsFrom, credentialPickerMode, connectionLabel, fieldIsVisible, jsonTextError, curlImportConfig } from './nodeInspectorRules.js'
import { CollectionField, AnyPlatformConnections, CurlImport } from './NodeInspectorWidgets.jsx'
import { onAutomationsChanged } from '../lib/appEvents.js'
import { arrangePaletteCategories, DEPRECATED, paletteCategoryLabel, paletteSectionOf, splitPaletteSections } from './paletteSections.js'
import { runProblem } from '../lib/runFailure.js'
import RunProblemPanel from '../components/RunProblemPanel.jsx'
import { rememberTriggerInput, rememberedTriggerInput } from './triggerInput.js'
import { usePageVisibleRef } from '../lib/usePageVisible.js'
import { isAgentNotSetup, withoutAgentSetupMarker } from '../lib/agentSetup.js'
import AgentSetupLink from '../components/AgentSetupLink.jsx'

// ── Wails bindings with mock fallback ────────────────────────────────────────
const RunNode               = WailsApp.RunNode               ?? (async (req) => ({ outputs: [{ handle: 'main', items: [{ mock: true, node_type: req.node_type }] }], duration_ms: 42 }))
const GetWorkflowNodeTypes  = WailsApp.GetWorkflowNodeTypes  ?? (async () => ({}))
const _LS = 'monoagent-wf-mock-v2'
const _ms = () => { try { return JSON.parse(localStorage.getItem(_LS) || '{}') } catch { return {} } }
const _mp = s  => { try { localStorage.setItem(_LS, JSON.stringify(s)) } catch {} }
const GetWorkflow      = WailsApp.GetWorkflow      ?? (async (id) => _ms()[id] || null)
const SaveWorkflow     = WailsApp.SaveWorkflow     ?? (async (req) => { const s=_ms(); const id=req.id||`wf_${Date.now()}`; const now=new Date().toISOString(); const ex=s[id]||{}; const n={...ex,...req,id,updated_at:now,created_at:ex.created_at||now,version:(ex.version||0)+1}; s[id]=n; _mp(s); return n })
const DeleteWorkflow   = WailsApp.DeleteWorkflow   ?? (async (id) => { const s=_ms(); delete s[id]; _mp(s) })
const SetWorkflowActive= WailsApp.SetWorkflowActive?? (async (id,a) => { const s=_ms(); if(s[id]){s[id].active=a;_mp(s)} })
const GetWorkflowExecutions = WailsApp.GetWorkflowExecutions ?? (async () => [])

// ── Canvas geometry ───────────────────────────────────────────────────────────
const NODE_W   = 220
const HEAD_H   = 44
const PORT_H   = 28
const PORT_PAD = 8
const PORT_R   = 6

const CAT_COLOR = {
  triggers:       '#7c3aed',
  control:        '#0891b2',
  data:           '#d97706',
  http:           '#d97706',
  system:         '#64748b',
  database:       '#1d4ed8',
  communication:  '#9333ea',
  services:       '#0f766e',
  instagram:      '#e1306c',
  linkedin:       '#0a66c2',
  x:              '#8899aa',
  tiktok:         '#ff0050',
  gemini:         '#4285f4',
  hackernews:     '#ff6600',
  producthunt:    '#da552f',
  org:            '#6366f1',
}
const catColor = (cat) => CAT_COLOR[cat] || '#00b4d8'
const LOOP_EDGE_COLOR = '#f59e0b'

function nodeH(n) {
  return HEAD_H + PORT_PAD + Math.max(n.inputs.length, n.outputs.length, 1) * PORT_H + PORT_PAD
}
function inPortPos(n, i) {
  return { x: n.x, y: n.y + HEAD_H + PORT_PAD + i * PORT_H + PORT_H / 2 }
}
function outPortPos(n, i) {
  return { x: n.x + NODE_W, y: n.y + HEAD_H + PORT_PAD + i * PORT_H + PORT_H / 2 }
}

// Canvas ids. Loading a workflow advances the counter past its nr<N> ids;
// taken (a Set) skips any id already on the canvas.
const canvasIds = makeIdGen('nr')
const uid = (taken) => canvasIds.next(taken)

// Parse Go time.String() format: "2026-04-08 19:55:14.260072 +0000 UTC"
// Also handles SQLite CURRENT_TIMESTAMP: "2026-04-08 19:55:14"
const parseGoTime = (s) => {
  if (!s) return null
  const iso = s.replace(' +0000 UTC', 'Z').replace(/^(\d{4}-\d{2}-\d{2}) /, '$1T')
  const d = new Date(iso)
  return isNaN(d.getTime()) ? null : d
}

// ── Status badge on node ──────────────────────────────────────────────────────
function NodeStatusBadge({ status, itemCount, durationMs }) {
  if (!status) return null
  const color = status === 'ok' ? '#10b981' : status === 'error' ? '#ef4444' : status === 'skipped' ? '#6b7280' : status === 'waiting' ? '#f59e0b' : '#00b4d8'
  const icon = status === 'ok' ? '✓' : status === 'error' ? '✕' : status === 'skipped' ? '—' : status === 'waiting' ? '⏸' : '…'
  return (
    <div style={{
      position: 'absolute', top: -10, right: -6,
      background: color,
      color: '#fff',
      fontFamily: 'var(--font-mono)',
      fontSize: 9,
      borderRadius: 10,
      padding: '2px 6px',
      display: 'flex', alignItems: 'center', gap: 3,
      boxShadow: `0 0 8px ${color}66`,
      whiteSpace: 'nowrap',
    }}>
      {icon} {status === 'ok' ? `${itemCount} item${itemCount !== 1 ? 's' : ''}` : status === 'running' ? 'running' : status === 'waiting' ? 'waiting' : status === 'skipped' ? 'skipped' : 'error'}
      {durationMs != null && !isNaN(durationMs) && status === 'ok' && <span style={{ opacity: 0.7 }}> · {durationMs}ms</span>}
    </div>
  )
}

// ── Single canvas node ────────────────────────────────────────────────────────
function CanvasNode({ node, selected, zoom, onHeaderMouseDown, onOutputPortMouseDown, onInputPortMouseUp, onClick, onDelete, onConfigure }) {
  const h = nodeH(node)
  const color = node.color || catColor(node.category)
  const status = node.runStatus // 'running' | 'ok' | 'error' | null
  const rows = Math.max(node.inputs.length, node.outputs.length, 1)

  return (
    <div
      style={{
        position: 'absolute', left: node.x, top: node.y,
        width: NODE_W, height: h,
        background: 'linear-gradient(160deg,#0d1a28 0%,#091220 100%)',
        border: `1.5px solid ${selected ? color : status === 'error' ? '#ef444444' : status === 'ok' ? '#10b98133' : status === 'skipped' ? '#6b728033' : status === 'waiting' ? '#f59e0b66' : status === 'running' ? '#00b4d866' : 'rgba(0,180,216,0.12)'}`,
        borderRadius: 10,
        boxShadow: selected
          ? `0 0 0 1.5px ${color}55, 0 12px 32px rgba(0,0,0,.7)`
          : '0 6px 20px rgba(0,0,0,.5)',
        userSelect: 'none',
        overflow: 'visible',
        pointerEvents: 'auto',
        transition: 'border-color 140ms, box-shadow 140ms',
      }}
      onMouseDown={(e) => { e.stopPropagation(); onClick?.() }}
    >
      <NodeStatusBadge
        status={status}
        itemCount={node.runOutputItems}
        durationMs={node.runDuration}
      />

      {/* Header */}
      <div
        style={{
          height: HEAD_H,
          background: `linear-gradient(110deg,${color}1a 0%,${color}0a 100%)`,
          borderBottom: `1px solid ${color}22`,
          borderRadius: '9px 9px 0 0',
          display: 'flex', alignItems: 'center',
          padding: '0 8px 0 10px',
          cursor: 'grab', gap: 6,
        }}
        onMouseDown={(e) => {
          if (e.target.closest('button')) return
          e.stopPropagation(); onHeaderMouseDown(e)
        }}
      >
        <div style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }} />
        <span style={{
          flex: 1,
          fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600,
          color: '#e2e8f0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}>{node.label}</span>
        <button
          onMouseDown={e => e.stopPropagation()}
          onClick={e => { e.stopPropagation(); onConfigure() }}
          title="Configure"
          style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'rgba(148,163,184,.5)', padding: 2, display: 'flex', alignItems: 'center', transition: 'color 100ms' }}
          onMouseEnter={e => e.currentTarget.style.color = '#00b4d8'}
          onMouseLeave={e => e.currentTarget.style.color = 'rgba(148,163,184,.5)'}
        ><Settings2 size={12} /></button>
        <button
          onMouseDown={e => e.stopPropagation()}
          onClick={e => { e.stopPropagation(); onDelete() }}
          title="Delete node"
          style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'rgba(148,163,184,.3)', padding: 2, display: 'flex', alignItems: 'center', transition: 'color 100ms' }}
          onMouseEnter={e => e.currentTarget.style.color = '#ef4444'}
          onMouseLeave={e => e.currentTarget.style.color = 'rgba(148,163,184,.3)'}
        ><X size={11} /></button>
      </div>

      {/* Ports */}
      <div style={{ position: 'relative', padding: `${PORT_PAD}px 0` }}>
        {Array.from({ length: rows }).map((_, i) => (
          <div key={i} style={{ height: PORT_H, display: 'flex', alignItems: 'center', justifyContent: 'space-between', position: 'relative' }}>
            {/* Input port */}
            {node.inputs[i] ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                <div
                  onMouseUp={e => { e.stopPropagation(); onInputPortMouseUp(i) }}
                  style={{
                    width: PORT_R * 2, height: PORT_R * 2,
                    borderRadius: '50%',
                    background: '#1e293b',
                    border: `1.5px solid ${color}66`,
                    cursor: 'crosshair',
                    marginLeft: -PORT_R,
                    transition: 'all 100ms',
                    flexShrink: 0,
                  }}
                  onMouseEnter={e => { e.currentTarget.style.background = color; e.currentTarget.style.borderColor = color }}
                  onMouseLeave={e => { e.currentTarget.style.background = '#1e293b'; e.currentTarget.style.borderColor = `${color}66` }}
                />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>{node.inputs[i].label}</span>
              </div>
            ) : <div />}

            {/* Output port */}
            {node.outputs[i] ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>{node.outputs[i].label}</span>
                <div
                  onMouseDown={e => { e.stopPropagation(); onOutputPortMouseDown(e, i) }}
                  style={{
                    width: PORT_R * 2, height: PORT_R * 2,
                    borderRadius: '50%',
                    background: '#1e293b',
                    border: `1.5px solid ${color}66`,
                    cursor: 'crosshair',
                    marginRight: -PORT_R,
                    transition: 'all 100ms',
                    flexShrink: 0,
                  }}
                  onMouseEnter={e => { e.currentTarget.style.background = color; e.currentTarget.style.borderColor = color }}
                  onMouseLeave={e => { e.currentTarget.style.background = '#1e293b'; e.currentTarget.style.borderColor = `${color}66` }}
                />
              </div>
            ) : <div />}
          </div>
        ))}
      </div>

      {/* Running pulse */}
      {status === 'running' && (
        <div style={{
          position: 'absolute', inset: 0, borderRadius: 10,
          border: `1.5px solid ${color}`,
          animation: 'nodePulse 1s ease-in-out infinite',
          pointerEvents: 'none',
        }} />
      )}
    </div>
  )
}

// ── Save workflow modal ───────────────────────────────────────────────────────

// ── Node palette sidebar ──────────────────────────────────────────────────────
function Palette({ categories, onAdd, onNodeMouseDown, onBrowseAutomations }) {
  const [search, setSearch] = useState('')
  const [open, setOpen] = useState(() => {
    try { return JSON.parse(localStorage.getItem('nr2-palette-open') || '{}') } catch { return {} }
  })

  const toggle = (id, wasOpen) => setOpen(prev => {
    const next = { ...prev, [id]: !wasOpen }
    try { localStorage.setItem('nr2-palette-open', JSON.stringify(next)) } catch {}
    return next
  })

  const q = search.toLowerCase()
  const filtered = arrangePaletteCategories(categories).map(cat => ({
    ...cat,
    nodes: q ? cat.nodes.filter(n => n.label.toLowerCase().includes(q) || n.subtype.toLowerCase().includes(q) || cat.label.toLowerCase().includes(q)) : cat.nodes,
  })).filter(cat => cat.nodes.length > 0)
  const sections = splitPaletteSections(filtered)

  return (
    <div style={{
      width: 200, flexShrink: 0,
      background: '#080d16',
      borderRight: '1px solid rgba(0,180,216,0.1)',
      display: 'flex', flexDirection: 'column',
      overflow: 'hidden',
    }}>
      <div style={{ padding: '10px 10px 8px', borderBottom: '1px solid rgba(0,180,216,0.08)', flexShrink: 0 }}>
        <div style={{ position: 'relative' }}>
          <Search size={11} style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)', pointerEvents: 'none' }} />
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search nodes…"
            style={{
              width: '100%', background: '#020509',
              border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6,
              padding: '5px 8px 5px 26px', color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 11, outline: 'none', boxSizing: 'border-box',
            }}
          />
        </div>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '4px 0 12px' }}>
        {sections.map(sec => (
          <div key={sec.id} data-testid={`palette-section-${sec.id}`}>
            <div style={{
              padding: '10px 10px 4px', fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700,
              letterSpacing: 1.8, textTransform: 'uppercase', color: 'var(--text-muted)',
              borderTop: sec.id === 'nodes' ? '1px solid rgba(0,180,216,0.08)' : 'none',
            }}>
              {sec.label}
            </div>
            {sec.categories.length === 0 && (
              <div style={{ padding: '2px 10px 8px', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', lineHeight: 1.5 }}>
                {search ? 'No matches' : <>
                  None installed —{' '}
                  <button onClick={onBrowseAutomations} style={{
                    background: 'none', border: 'none', padding: 0, cursor: 'pointer',
                    fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00b4d8', textDecoration: 'underline', textAlign: 'left',
                  }}>browse and install web automations</button>
                </>}
              </div>
            )}
        {sec.categories.map(cat => {
          const isOpen = search ? true : (open[cat.id] ?? !cat.collapsedByDefault)
          const color = cat.id === DEPRECATED ? '#6b7280' : catColor(cat.id)
          return (
            <div key={cat.id}>
              <div
                onClick={() => !search && toggle(cat.id, isOpen)}
                style={{ display: 'flex', alignItems: 'center', gap: 5, padding: '7px 10px 5px', cursor: search ? 'default' : 'pointer', userSelect: 'none' }}
              >
                {!search && (isOpen ? <ChevronDown size={9} color={color} /> : <ChevronRight size={9} color={color} />)}
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color, letterSpacing: 1.5, textTransform: 'uppercase' }}>
                  {cat.label}
                </span>
              </div>
              {isOpen && cat.nodes.map(n => (
                <div
                  key={n.subtype}
                  onMouseDown={e => { e.preventDefault(); onNodeMouseDown(e, { ...n, category: n.category || cat.id }) }}
                  onClick={() => onAdd({ ...n, category: n.category || cat.id })}
                  title={`Click or drag to add ${n.label}`}
                  style={{
                    padding: '5px 14px 5px 20px',
                    cursor: 'grab',
                    fontFamily: 'var(--font-mono)', fontSize: 11,
                    color: 'var(--text-secondary)',
                    borderLeft: '2px solid transparent',
                    transition: 'all 80ms',
                    whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
                    display: 'flex', alignItems: 'center', gap: 6,
                  }}
                  onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.04)'; e.currentTarget.style.borderLeftColor = color; e.currentTarget.style.color = '#e2e8f0' }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.borderLeftColor = 'transparent'; e.currentTarget.style.color = 'var(--text-secondary)' }}
                >
                  <div style={{ width: 5, height: 5, borderRadius: '50%', background: color, flexShrink: 0 }} />
                  {n.label}
                </div>
              ))}
            </div>
          )
        })}
          </div>
        ))}
      </div>
    </div>
  )
}

// ── Inspector panel (right side) ──────────────────────────────────────────────
// liveSchema resolves a node's schema from the node catalog rather than the
// copy saved inside the workflow. Schemas are code: a workflow file keeps the
// snapshot taken when it was saved, so without this every schema improvement
// — a new field, better help, the example prompts a user is meant to start
// from — stays invisible in every workflow saved before it. The stored copy
// remains the fallback for a node type the catalog doesn't know.
function resolveSchema(node, liveSchemas) {
  return liveSchemas?.[node?.subtype] || node?.schema || null
}

function Inspector({ node, onConfigChange, onClose, onNavigate, liveSchemas, automationIds }) {
  const schema = resolveSchema(node, liveSchemas)
  const [copied, setCopied] = useState(false)
  const [connections, setConnections] = useState([])
  const [loadingCreds, setLoadingCreds] = useState(false)
  const [vaultImages, setVaultImages] = useState([])
  const [atAC, setAtAC] = useState({ open: false, query: '', fieldKey: null })
  const [pickerField, setPickerField] = useState(null)

  const platformId = derivePlatformId(node, schema, automationIds)
  const isBrowserPlatform = isSessionPicker(node, automationIds)
  // 'any': a generic HTTP/GraphQL node takes a connection of any platform.
  const pickerMode = credentialPickerMode(platformId, schema)

  useEffect(() => {
    if (!pickerMode) { setConnections([]); return }
    setLoadingCreds(true)
    const load = pickerMode === 'any' ? api.listConnections('') : api.getConnectionsForPlatform(platformId)
    load
      .then(list => {
        const conns = Array.isArray(list) ? list : []
        setConnections(conns)
        // Auto-select the first (or only) credential if none is set. Not in
        // any-platform mode: there a connection is optional and adds auth.
        if (pickerMode === 'platform' && conns.length > 0 && !node.config?.credential_id) {
          const firstId = String(conns[0].id || conns[0].ID || '')
          if (firstId) onConfigChange(node.id, 'credential_id', firstId)
        }
      })
      .catch(() => setConnections([]))
      .finally(() => setLoadingCreds(false))
  }, [platformId, pickerMode, node?.id])

  useEffect(() => {
    WailsApp.GetVaultImages(50).then(imgs => setVaultImages(imgs || [])).catch(() => {})
  }, [node?.id])

  if (!node) return null

  const color = node.color || catColor(node.category)
  const outputItems = node.runOutputs?.flatMap(o => o.items) ?? []

  const copyOutput = () => {
    navigator.clipboard.writeText(JSON.stringify(node.runOutputs, null, 2))
    setCopied(true); setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div style={{
      width: 280, flexShrink: 0,
      background: '#060b13', borderLeft: '1px solid rgba(0,180,216,0.08)',
      display: 'flex', flexDirection: 'column', overflow: 'hidden',
    }}>
      {/* Header */}
      <div style={{
        padding: '10px 12px', flexShrink: 0,
        borderBottom: '1px solid rgba(0,180,216,0.08)',
        display: 'flex', alignItems: 'center', gap: 8,
      }}>
        <div style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }}>{node.label}</span>
        <button onClick={onClose} aria-label="Close inspector" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', display: 'flex', padding: 2 }}
          onMouseEnter={e => e.currentTarget.style.color = '#fff'}
          onMouseLeave={e => e.currentTarget.style.color = 'var(--text-muted)'}
        ><X size={12} /></button>
      </div>

      {/* overflowWrap: long unbroken words (option ids in help text) wrap
          instead of running past the inspector's right edge. */}
      <div style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', overflowWrap: 'anywhere', padding: '12px' }}>
        {/* Credential dropdown — shown for platforms that require auth */}
        {pickerMode && (
          <>
            <Label>{isBrowserPlatform ? 'SESSION' : 'CREDENTIAL'}</Label>
            <div style={{ marginBottom: 10 }}>
              <select
                value={node.config?.credential_id ?? ''}
                onChange={e => onConfigChange(node.id, 'credential_id', String(e.target.value))}
                className="select-compact"
                style={selectStyle}
                disabled={loadingCreds}
              >
                <option value="">— None —</option>
                {pickerMode === 'any'
                  ? <AnyPlatformConnections connections={connections} />
                  : connections.map(c => {
                    const id = c.id || c.ID || ''
                    return <option key={id} value={String(id)}>{connectionLabel(c)}</option>
                  })}
              </select>
              {pickerMode === 'any' && (
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', marginTop: 4 }}>
                  Any saved connection, grouped by platform. Optional.
                </div>
              )}
              {loadingCreds && (
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', marginTop: 4 }}>
                  Loading…
                </div>
              )}
              <button
                onClick={() => onNavigate?.('connections')}
                style={{
                  background: 'transparent', border: 'none', cursor: 'pointer',
                  fontFamily: 'var(--font-mono)', fontSize: 9,
                  color: '#00b4d8', padding: '4px 0', display: 'block', marginTop: 4,
                }}
                onMouseEnter={e => e.currentTarget.style.opacity = '0.7'}
                onMouseLeave={e => e.currentTarget.style.opacity = '1'}
              >
                Manage credentials →
              </button>
            </div>
          </>
        )}

        {/* Config fields */}
        {(() => {
          const fields = schema?.fields || node.configFields || []
          if (fields.length === 0) {
            return (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', marginBottom: 12 }}>
                No config fields — this node runs with defaults.
              </div>
            )
          }
          return (
            <>
              <Label>CONFIG</Label>
              {node.subtype === 'http.request' && (
                <CurlImport
                  inputStyle={inputStyle}
                  onImport={imported => {
                    const next = curlImportConfig(fields, node.config, imported)
                    Object.entries(next).forEach(([k, v]) => onConfigChange(node.id, k, v))
                  }}
                />
              )}
              {fields.map(f => {
                if (!fieldIsVisible(f, node.config, fields)) return null
                // Skip credential_id text field when a credential picker is rendered above
                if (f.key === 'credential_id' && pickerMode) return null
                const rawVal0 = node.config?.[f.key] ?? f.default ?? ''
                // A list/object value in a text editor (e.g. core.switch cases
                // edited in the textarea fallback) shows as JSON, not "[object Object]".
                const val = rawVal0 !== null && typeof rawVal0 === 'object' ? JSON.stringify(rawVal0) : rawVal0
                const onChange = e => onConfigChange(node.id, f.key, e.target.value)
                let inputEl
                if (f.type === 'boolean') {
                  const checked = Boolean(node.config?.[f.key] ?? f.default ?? false)
                  inputEl = (
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={e => onConfigChange(node.id, f.key, e.target.checked)}
                      style={{ accentColor: '#00b4d8', width: 14, height: 14 }}
                    />
                  )
                } else if (f.type === 'textarea') {
                  inputEl = (
                    <textarea
                      value={val}
                      onChange={onChange}
                      rows={f.rows || 3}
                      placeholder={f.placeholder || ''}
                      style={inputStyle}
                    />
                  )
                } else if (f.type === 'code') {
                  return (
                    <div key={f.key} className="config-field" style={{ marginBottom: 10 }}>
                      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.2, textTransform: 'uppercase', marginBottom: 4 }}>
                        {f.label}{f.required ? ' *' : ''}
                        {f.language && <span className="code-lang-badge">{f.language}</span>}
                      </div>
                      <textarea
                        className="field-code"
                        rows={f.rows || 10}
                        value={val}
                        onChange={e => onConfigChange(node.id, f.key, e.target.value)}
                        placeholder={f.placeholder || `// Enter ${f.language || ''} code here`}
                        spellCheck={false}
                        autoComplete="off"
                        autoCorrect="off"
                        style={{ ...inputStyle, fontFamily: 'var(--font-mono)', fontSize: 11 }}
                      />
                      {f.language === 'json' && jsonTextError(val) && (
                        <div role="alert" style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: '#f87171', marginTop: 4 }}>{jsonTextError(val)}</div>
                      )}
                      {f.help && <p className="field-help">{f.help}</p>}
                    </div>
                  )
                } else if (f.type === 'collection') {
                  inputEl = (
                    <CollectionField
                      key={`${node.id}:${f.key}`}
                      field={f}
                      value={node.config?.[f.key]}
                      onCommit={v => onConfigChange(node.id, f.key, v)}
                      inputStyle={inputStyle}
                    />
                  )
                } else if (f.type === 'select') {
                  inputEl = (
                    <select
                      value={val}
                      onChange={onChange}
                      className="select-compact"
                      style={selectStyle}
                    >
                      {(f.options || []).map(o => <option key={o} value={o}>{o}</option>)}
                    </select>
                  )
                } else if (f.type === 'number') {
                  inputEl = (
                    <input
                      type="number"
                      min={f.min}
                      max={f.max}
                      value={val}
                      onChange={onChange}
                      style={inputStyle}
                    />
                  )
                } else if (f.type === 'password') {
                  inputEl = (
                    <input
                      type="password"
                      value={val}
                      onChange={onChange}
                      style={inputStyle}
                    />
                  )
                } else if (f.type === 'array') {
                  const rawVal = node.config?.[f.key]
                  const arrValue = arrayFieldItems(rawVal)
                  return (
                    <div key={f.key} className="config-field" style={{ marginBottom: 10 }}>
                      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.2, textTransform: 'uppercase', marginBottom: 4 }}>
                        {f.label}{f.required ? ' *' : ''}
                      </div>
                      <input
                        type="text"
                        className="array-tag-input"
                        placeholder={f.placeholder || 'Type and press Enter...'}
                        style={inputStyle}
                        onKeyDown={e => {
                          if (e.key === 'Enter' && e.target.value.trim()) {
                            e.preventDefault()
                            const newArr = [...arrValue, e.target.value.trim()]
                            onConfigChange(node.id, f.key, newArr)
                            e.target.value = ''
                          }
                        }}
                      />
                      {arrValue.length > 0 && (
                        <div className="field-tags">
                          {arrValue.map((tag, i) => (
                            <span key={i} className="field-tag" style={{ maxWidth: '100%' }}>
                              <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{arrayTagLabel(tag)}</span>
                              <button
                                type="button"
                                className="field-tag-remove"
                                onClick={() => {
                                  const newArr = arrValue.filter((_, idx) => idx !== i)
                                  onConfigChange(node.id, f.key, newArr)
                                }}
                              >×</button>
                            </span>
                          ))}
                        </div>
                      )}
                      {f.examples?.length > 0 && (
                        <div className="field-examples">
                          <span className="field-examples-label">Examples — click to add</span>
                          {f.examples.map((ex, i) => (
                            <button
                              key={i}
                              type="button"
                              className="field-example"
                              title="Add this prompt"
                              onClick={() => onConfigChange(node.id, f.key, [...arrValue, ex])}
                            >{ex}</button>
                          ))}
                        </div>
                      )}
                      {f.help && <p className="field-help">{f.help}</p>}
                    </div>
                  )
                } else if (f.type === '_help') {
                  // Inline help/reference section — not a config field
                  return (
                    <details key={f.key} style={{ marginBottom: 12 }}>
                      <summary style={{
                        fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--cyan)',
                        cursor: 'pointer', letterSpacing: 0.5, userSelect: 'none',
                        padding: '4px 0',
                      }}>
                        {f.label}
                      </summary>
                      <div style={{
                        marginTop: 6, padding: '8px 10px',
                        background: 'rgba(0,180,216,0.04)',
                        border: '1px solid rgba(0,180,216,0.1)',
                        borderRadius: 6,
                      }}>
                        {(f.helpContent || []).map((section, si) => (
                          <div key={si} style={{ marginBottom: si < f.helpContent.length - 1 ? 10 : 0 }}>
                            <div style={{
                              fontFamily: 'var(--font-mono)', fontSize: 9,
                              color: 'var(--text-secondary)', letterSpacing: 1,
                              textTransform: 'uppercase', marginBottom: 4,
                            }}>
                              {section.title}
                            </div>
                            {section.examples.map((ex, ei) => (
                              <div key={ei} style={{
                                fontFamily: 'var(--font-mono)', fontSize: 10,
                                color: '#c4d1de', padding: '2px 6px',
                                background: 'rgba(0,0,0,0.3)', borderRadius: 3,
                                marginBottom: 3, wordBreak: 'break-all',
                                cursor: 'pointer',
                              }}
                                title="Click to copy"
                                onClick={() => {
                                  navigator.clipboard.writeText(ex)
                                }}
                              >
                                {ex}
                              </div>
                            ))}
                          </div>
                        ))}
                      </div>
                    </details>
                  )
                } else if (f.type === 'resource_picker') {
                  inputEl = (
                    <ResourcePickerField
                      field={f}
                      value={node.config?.[f.key] || ''}
                      onChange={v => onConfigChange(node.id, f.key, v)}
                      credentialId={node.config?.credential_id || ''}
                      platform={schema?.credential_platform || ''}
                      nodeConfig={node.config}
                    />
                  )
                } else {
                  // 'text' and any unknown types — with @-autocomplete and picker button
                  const media = isMediaField(f)
                  const acMatches = media && atAC.open && atAC.fieldKey === f.key
                    ? vaultImages.filter(img =>
                        img.id.includes(atAC.query) ||
                        (img.label || '').toLowerCase().includes(atAC.query.toLowerCase())
                      ).slice(0, 8)
                    : []

                  const handleAtChange = (e) => {
                    const v = e.target.value
                    onConfigChange(node.id, f.key, v)
                    const lastAt = media ? v.lastIndexOf('@') : -1
                    if (lastAt !== -1) {
                      const afterAt = v.slice(lastAt + 1)
                      if (!afterAt.includes(' ')) {
                        setAtAC({ open: true, query: afterAt, fieldKey: f.key })
                        return
                      }
                    }
                    setAtAC({ open: false, query: '', fieldKey: null })
                  }

                  inputEl = (
                    <div style={{ position: 'relative' }}>
                      <div style={{ display: 'flex', gap: 4 }}>
                        <input
                          type="text"
                          value={val}
                          onChange={handleAtChange}
                          onBlur={() => setTimeout(() => setAtAC({ open: false, query: '', fieldKey: null }), 150)}
                          placeholder={f.placeholder || ''}
                          style={{ ...inputStyle, flex: 1 }}
                        />
                        {media && <button
                          title="Pick from Image Vault"
                          onClick={() => setPickerField(f.key)}
                          style={{
                            background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 4,
                            padding: '0 7px', cursor: 'pointer', color: '#475569', flexShrink: 0,
                            display: 'flex', alignItems: 'center',
                          }}
                          onMouseEnter={e => e.currentTarget.style.color = '#00b4d8'}
                          onMouseLeave={e => e.currentTarget.style.color = '#475569'}
                        >
                          🖼
                        </button>}
                      </div>
                      {atAC.open && atAC.fieldKey === f.key && acMatches.length > 0 && (
                        <div style={{
                          position: 'absolute', top: '100%', left: 0, right: 0, zIndex: 200,
                          background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 5,
                          boxShadow: '0 4px 16px rgba(0,0,0,0.5)', overflow: 'hidden', marginTop: 2,
                        }}>
                          <div style={{ padding: '3px 8px', background: '#111827', fontFamily: 'var(--font-mono)', fontSize: 8, color: '#334155', textTransform: 'uppercase', letterSpacing: 1 }}>
                            Vault Images
                          </div>
                          {acMatches.map(img => (
                            <div
                              key={img.id}
                              onMouseDown={() => {
                                const v = String(val)
                                const lastAt = v.lastIndexOf('@')
                                const newVal = v.slice(0, lastAt) + '@' + img.id + ' '
                                onConfigChange(node.id, f.key, newVal)
                                setAtAC({ open: false, query: '', fieldKey: null })
                              }}
                              style={{
                                display: 'flex', alignItems: 'center', gap: 7,
                                padding: '5px 8px', cursor: 'pointer',
                              }}
                              onMouseEnter={e => e.currentTarget.style.background = '#0a1829'}
                              onMouseLeave={e => e.currentTarget.style.background = ''}
                            >
                              <div style={{ width: 20, height: 20, borderRadius: 2, overflow: 'hidden', background: '#060b11', flexShrink: 0 }}>
                                <img src={img.url} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} onError={e => { e.target.style.display = 'none' }} />
                              </div>
                              <div>
                                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00b4d8' }}>@{img.id}</div>
                                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 8, color: '#475569' }}>{img.label || img.filename}</div>
                              </div>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  )
                }
                return (
                  <div key={f.key} style={{ marginBottom: 10 }}>
                    <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.2, textTransform: 'uppercase', marginBottom: 4 }}>
                      {f.label}{f.required ? ' *' : ''}
                    </div>
                    {inputEl}
                    {f.examples?.length > 0 && (
                      <div className="field-examples">
                        <span className="field-examples-label">Examples — click to use</span>
                        {f.examples.map((ex, i) => (
                          <button
                            key={i}
                            type="button"
                            className="field-example"
                            title="Use this as the value"
                            onClick={() => onConfigChange(node.id, f.key, ex)}
                          >{ex}</button>
                        ))}
                      </div>
                    )}
                    {f.help && <p className="field-help">{f.help}</p>}
                  </div>
                )
              })}
            </>
          )
        })()}

        {/* Input data — what this node received from upstream */}
        {node.runStatus && node.runInputItems && node.runInputItems.length > 0 && (
          <>
            <div style={{ height: 1, background: 'rgba(0,180,216,0.08)', margin: '12px 0' }} />
            <details style={{ marginBottom: 4 }}>
              <summary style={{
                fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)',
                letterSpacing: 1.2, textTransform: 'uppercase', cursor: 'pointer',
                padding: '4px 0', userSelect: 'none',
              }}>
                INPUT · {node.runInputItems.length} item{node.runInputItems.length !== 1 ? 's' : ''}
              </summary>
              <div style={{ marginTop: 4 }}>
                {node.runInputItems.slice(0, 3).map((item, ii) => (
                  <div key={ii} style={{
                    background: '#020509', border: '1px solid rgba(107,114,128,0.15)',
                    borderRadius: 6, padding: '7px 9px', marginBottom: 4, position: 'relative',
                  }}>
                    {node.runInputItems.length > 1 && (
                      <span style={{ position: 'absolute', top: 4, right: 6, fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>[{ii}]</span>
                    )}
                    <pre style={{ margin: 0, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#a0aec0', whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: 100, overflow: 'auto' }}>
                      {JSON.stringify(item, null, 2)}
                    </pre>
                  </div>
                ))}
                {node.runInputItems.length > 3 && (
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textAlign: 'center', padding: '2px 0' }}>
                    + {node.runInputItems.length - 3} more
                  </div>
                )}
              </div>
            </details>
          </>
        )}

        {/* Output results */}
        {node.runStatus && (
          <>
            <div style={{ height: 1, background: 'rgba(0,180,216,0.08)', margin: '12px 0' }} />
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8 }}>
              <Label style={{ marginBottom: 0 }}>OUTPUT</Label>
              <div style={{ flex: 1 }} />
              {node.runStatus === 'ok' && (
                <button
                  onClick={copyOutput}
                  style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 9, display: 'flex', alignItems: 'center', gap: 3 }}
                  onMouseEnter={e => e.currentTarget.style.color = '#00b4d8'}
                  onMouseLeave={e => e.currentTarget.style.color = 'var(--text-muted)'}
                >
                  <Copy size={9} /> {copied ? 'COPIED' : 'COPY'}
                </button>
              )}
            </div>

            {node.runStatus === 'running' && (
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11 }}>
                <Loader size={11} style={{ animation: 'spin 0.7s linear infinite' }} /> Running…
              </div>
            )}
            {node.runStatus === 'waiting' && (
              <div style={{ background: 'rgba(245,158,11,0.08)', border: '1px solid rgba(245,158,11,0.25)', borderRadius: 6, padding: '8px 10px', display: 'flex', alignItems: 'center', gap: 5 }}>
                <Clock size={11} color="#f59e0b" />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#f59e0b' }}>WAITING — paused for human review</span>
              </div>
            )}
            {node.runStatus === 'error' && (
              <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 6, padding: '8px 10px' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginBottom: 4 }}>
                  <AlertCircle size={11} color="#ef4444" />
                  <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#ef4444' }}>ERROR</span>
                </div>
                <pre style={{ margin: 0, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                  {withoutAgentSetupMarker(node.runError)}
                </pre>
                {isAgentNotSetup(node.runError) && <AgentSetupLink onNavigate={onNavigate} />}
              </div>
            )}
            {node.runStatus === 'skipped' && (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#6b7280', fontStyle: 'italic' }}>skipped — no items received from upstream nodes</div>
            )}
            {node.runStatus === 'ok' && !node.runOutputs && (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', fontStyle: 'italic' }}>no output items</div>
            )}
            {node.runStatus === 'ok' && node.runOutputs?.map((out, oi) => (
              <div key={oi} style={{ marginBottom: 8 }}>
                {node.runOutputs.length > 1 && (
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: '#00b4d8', letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 4, display: 'flex', alignItems: 'center', gap: 4 }}>
                    <div style={{ width: 5, height: 5, borderRadius: '50%', background: '#00b4d8' }} />
                    {out.handle} · {out.items.length} item{out.items.length !== 1 ? 's' : ''}
                  </div>
                )}
                {out.items.slice(0, 5).map((item, ii) => (
                  <div key={ii} style={{ background: '#020509', border: '1px solid rgba(0,180,216,0.1)', borderRadius: 6, padding: '7px 9px', marginBottom: 5, position: 'relative' }}>
                    {out.items.length > 1 && (
                      <span style={{ position: 'absolute', top: 4, right: 6, fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>[{ii}]</span>
                    )}
                    <pre style={{ margin: 0, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#e2e8f0', whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: 120, overflow: 'auto' }}>
                      {JSON.stringify(item, null, 2)}
                    </pre>
                  </div>
                ))}
                {out.items.length > 5 && (
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textAlign: 'center', padding: '4px 0' }}>
                    + {out.items.length - 5} more items
                  </div>
                )}
              </div>
            ))}
            {node.runStatus === 'ok' && node.runDuration != null && !isNaN(node.runDuration) && (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', display: 'flex', alignItems: 'center', gap: 4, marginTop: 6 }}>
                <Clock size={10} /> {node.runDuration}ms
              </div>
            )}
          </>
        )}
      </div>
      {pickerField && (
        <ImagePickerModal
          onSelect={(ref) => {
            onConfigChange(node.id, pickerField, ref)
            setPickerField(null)
          }}
          onClose={() => setPickerField(null)}
        />
      )}
    </div>
  )
}

const inputStyle = {
  width: '100%', background: '#020509',
  border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6,
  padding: '6px 8px', color: '#e2e8f0',
  fontFamily: 'var(--font-mono)', fontSize: 11, outline: 'none',
  boxSizing: 'border-box', resize: 'vertical',
}

// <select>s take their look from the global `select` rule plus
// `.select-compact` (index.css); only layout goes inline, and never
// inputStyle's `background` shorthand, which would wipe the chevron.
const selectStyle = { width: '100%', fontSize: 11 }

function Label({ children, style }) {
  return (
    <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 2, textTransform: 'uppercase', marginBottom: 8, ...style }}>
      {children}
    </div>
  )
}

// ── Resolve a stored connection handle ('main', 'true', …) to a port index ───
// Handles are port ids, not indexes; legacy files stored numeric strings.
function resolvePortIdx(ports, handle) {
  if (handle == null) return 0
  const byId = (ports || []).findIndex(p => p.id === handle)
  if (byId !== -1) return byId
  const n = parseInt(handle, 10)
  return isNaN(n) ? 0 : n
}

// Default config a freshly dropped node starts with (mirrors addNode).
function schemaDefaults(fields) {
  const d = {}
  ;(fields || []).forEach(f => { d[f.key] = f.default ?? '' })
  return d
}

// ── Main page ─────────────────────────────────────────────────────────────────
export default function NodeRunner({ onNavigate, navData, onWorkflowsChanged }) {
  const [categories, setCategories] = useState([])
  // type -> schema, from the node catalog. The workflow file carries the
  // schema each node had when it was saved; the catalog is what the running
  // build actually defines, so the inspector reads from here (resolveSchema).
  const liveSchemas = useMemo(() => {
    const byType = {}
    for (const cat of categories) {
      for (const n of cat.nodes || []) {
        if (n.subtype && n.schema) byType[n.subtype] = n.schema
      }
    }
    return byType
  }, [categories])
  const [nodes, setNodes]           = useState([])
  const [edges, setEdges]           = useState([])
  const [selectedId, setSelectedId] = useState(null)
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [camera, setCamera]         = useState({ x: 60, y: 60, zoom: 1 })
  const [pendingEdge, setPendingEdge] = useState(null)
  const [running, setRunning]       = useState(false)
  const stopRef                     = useRef(false)
  const [globalStatus, setGlobalStatus] = useState(null) // null | 'ok' | 'error'

  // ── Workflow persistence state ────────────────────────────────────────────
  const [wfId,     setWfId]     = useState(() => {
    try { return localStorage.getItem('nr2-last-wf-id') || null } catch { return null }
  })
  const [wfName,   setWfName]   = useState('Untitled Workflow')
  const [wfActive, setWfActive] = useState(false)
  const [saving,        setSaving]       = useState(false)
  const [saveMsg,       setSaveMsg]       = useState(null) // { ok: bool, text: string }
  const [showWfModal,   setShowWfModal]   = useState(false) // false | 'saved' | 'templates'
  const [showImport,    setShowImport]    = useState(false)
  const [showPublish,   setShowPublish]   = useState(false)
  const [showSaveModal, setShowSaveModal] = useState(false)
  // { asked, fields, value } — asked flips once the modal has been answered
  // for this run, so a re-run doesn't re-prompt mid-flight.
  const [triggerInput, setTriggerInput] = useState({ asked: false, fields: [], value: '' })
  const [isDirty, setIsDirty] = useState(false)

  // Persist last loaded workflow ID so it restores on next visit
  useEffect(() => {
    try {
      if (wfId) localStorage.setItem('nr2-last-wf-id', wfId)
      else localStorage.removeItem('nr2-last-wf-id')
    } catch {}
  }, [wfId])

  const [jsonView, setJsonView] = useState(false)

  // ── Execution overlay ───────────────────────────────────────────────────
  const [execOverlay, setExecOverlay] = useState(null) // { id, status, nodes: [], problem }
  const [problemHidden, setProblemHidden] = useState(false) // the user closed the error panel
  const pollRef = useRef(null) // setInterval id for execution polling
  const pageVisibleRef = usePageVisibleRef() // gates poll ticks while hidden

  // Shared: map execution detail → node badges
  const applyExecDetail = (detail) => {
    if (!detail) return
    // Why a run failed: the execution's error plus each failed node's, with
    // the node an engine-level error names (e.g. an unknown node type that
    // never got to execute) marked on the canvas too.
    const problem = runProblem(detail, nodesRef.current || [])
    const inferred = new Map((problem?.failed || []).filter(f => f.inferred).map(f => [f.id, f.error]))
    setExecOverlay({ id: detail.id, status: detail.status, nodes: detail.nodes || [], hint: detail.hint || null, error: detail.error || '', problem })
    setNodes(prev => {
      const byId = {}; const byName = {}
      ;(detail.nodes || []).forEach(en => { byId[en.node_id] = en; byName[en.node_name] = en })
      return prev.map(n => {
        const en = byId[n.id] || byName[n.label]
        if (!en && inferred.has(n.id)) return { ...n, runStatus: 'error', runError: inferred.get(n.id), runDuration: null, runOutputItems: 0, runInputItems: null, runOutputs: null }
        if (!en) return { ...n, runStatus: null, runError: null, runDuration: null, runOutputItems: 0, runInputItems: null, runOutputs: null }
        const s = (en.status || '').toUpperCase()
        let runStatus = null
        if (s === 'SUCCESS' || s === 'COMPLETED') runStatus = 'ok'
        else if (s === 'RUNNING') runStatus = 'running'
        else if (s === 'WAITING') runStatus = 'waiting'
        else if (s === 'FAILED') runStatus = 'error'
        else if (s === 'SKIPPED') runStatus = 'skipped'
        let durationMs = null
        if (en.started_at && en.finished_at) {
          const t1 = parseGoTime(en.started_at), t2 = parseGoTime(en.finished_at)
          if (t1 && t2) durationMs = Math.round(t2 - t1)
        }
        let inputItems = null; let outputItems = null; let outputCount = 0
        try { inputItems = JSON.parse(en.input_items || '[]') } catch {}
        try { outputItems = JSON.parse(en.output_items || '[]'); outputCount = outputItems.length } catch {}
        return {
          ...n, runStatus, runError: en.error_message || null, runDuration: durationMs,
          runInputItems: inputItems,
          runOutputs: outputItems?.length ? [{ handle: 'main', items: outputItems }] : null,
          runOutputItems: outputCount,
        }
      })
    })
  }

  // Stop any active execution poll: clear the interval, the running flag,
  // per-node run state and the execution overlay. Callers that only want to
  // replace the poll (startExecPoll) manage the interval themselves.
  const stopPolling = () => {
    if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
    setRunning(false)
    setExecOverlay(null)
    setNodes(prev => prev.map(n => ({ ...n, runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null })))
  }

  // Start polling an execution by ID. Returns a cleanup function.
  const startExecPoll = (execId) => {
    if (pollRef.current) clearInterval(pollRef.current)
    // Immediate first fetch
    api.getExecutionDetail(execId).then(d => d && applyExecDetail(d)).catch(() => {})
    const iv = setInterval(async () => {
      // Tick gated on page visibility — a hidden window stops polling; the
      // next tick after returning visible picks the execution back up.
      if (!pageVisibleRef.current) return
      try {
        const detail = await api.getExecutionDetail(execId)
        if (!detail) return
        applyExecDetail(detail)
        const st = (detail.status || '').toUpperCase()
        // WAITING (paused at a Human-in-Loop node) may resume after approval,
        // so keep polling it alongside the active statuses.
        if (st !== 'RUNNING' && st !== 'QUEUED' && st !== 'PENDING' && st !== 'WAITING') {
          clearInterval(iv)
          if (pollRef.current === iv) pollRef.current = null
          setRunning(false)
          setGlobalStatus(st === 'SUCCESS' || st === 'COMPLETED' ? 'ok' : st === 'SUCCESS_WITH_ERRORS' ? 'warning' : st === 'WAITING' ? 'waiting' : 'error')
        }
      } catch (e) { console.error('Poll error', e) }
    }, 2000)
    pollRef.current = iv
  }

  // Cleanup polling on unmount
  useEffect(() => () => { if (pollRef.current) clearInterval(pollRef.current) }, [])

  // When arriving from Dashboard with only a workflowId (no executionId —
  // e.g. clicking a workflow row that has never run yet), load that
  // workflow directly. Mirrors handleLoad below (defined later in this
  // component, so not callable from here) rather than reordering it.
  useEffect(() => {
    if (!navData?.workflowId || navData?.executionId) return
    let cancelled = false
    ;(async () => {
      try {
        const wf = await WailsApp.GetWorkflow(navData.workflowId)
        if (cancelled || !wf) return
        const prefixToCat = { core: 'control', db: 'database', comm: 'communication', service: 'services', data: 'data', http: 'http', system: 'system', trigger: 'triggers', ai: 'ai', instagram: 'instagram', linkedin: 'linkedin', x: 'x', tiktok: 'tiktok' }
        const loadedNodes = (wf.nodes || []).map(n => {
          const nt = normalizeNodeType(n.node_type || n.type || '')
          const prefix = nt.split('.')[0]
          const cat = prefixToCat[prefix] || prefix
          return {
            id: n.id, label: n.name, subtype: nt, category: cat,
            x: n.position_x, y: n.position_y,
            config: n.config || {}, color: catColor(cat),
            schema: n.schema || null,
            configFields: n.schema?.fields ? n.schema.fields : getConfigFields(nt),
            inputs: deriveInputs(nt), outputs: deriveOutputs(nt, n.config),
            runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null,
          }
        })
        const nodeById = {}
        loadedNodes.forEach(n => { nodeById[n.id] = n })
        const loadedEdges = (wf.connections || []).map(c => ({
          id: c.id, source: c.source_node_id, sourcePortId: canonicalOutputHandle(nodeById[c.source_node_id]?.subtype, c.source_handle),
          sourcePortIdx: resolvePortIdx(nodeById[c.source_node_id]?.outputs, canonicalOutputHandle(nodeById[c.source_node_id]?.subtype, c.source_handle)),
          target: c.target_node_id, targetPortId: c.target_handle,
          targetPortIdx: resolvePortIdx(nodeById[c.target_node_id]?.inputs, c.target_handle),
        }))
        setWfId(wf.id)
        setWfName(wf.name || 'Untitled Workflow')
        setWfActive(!!wf.is_active)
        canvasIds.advancePast([...loadedNodes, ...loadedEdges].map(x => x.id))
        setNodes(loadedNodes)
        setEdges(loadedEdges)
        setSelectedId(null)
        setIsDirty(false)
        setCamera({ x: 60, y: 60, zoom: 1 })
      } catch (e) { console.error('Failed to load workflow from navData', e) }
    })()
    return () => { cancelled = true }
  }, [navData?.workflowId, navData?.executionId]) // eslint-disable-line react-hooks/exhaustive-deps

  // The dashboard's "Get started" sends { openTemplates: true }.
  useEffect(() => { if (navData?.openTemplates) setShowWfModal('templates') }, [navData])

  // When arriving from Dashboard with an executionId, load workflow + overlay
  useEffect(() => {
    if (!navData?.executionId) return
    let cancelled = false
    ;(async () => {
      try {
        const detail = await api.getExecutionDetail(navData.executionId)
        if (cancelled || !detail) return
        // Load workflow if needed
        if (detail.workflow_id && (detail.workflow_id !== wfId || nodes.length === 0)) {
          const wf = await WailsApp.GetWorkflow(detail.workflow_id)
          if (wf && !cancelled) {
            const prefixToCat = { core: 'control', db: 'database', comm: 'communication', service: 'services', data: 'data', http: 'http', system: 'system', trigger: 'triggers', ai: 'ai', instagram: 'instagram', linkedin: 'linkedin', x: 'x', tiktok: 'tiktok' }
            const loadedNodes = (wf.nodes || []).map(n => {
              const nt = normalizeNodeType(n.node_type || n.type || '')
              const prefix = nt.split('.')[0]
              const cat = prefixToCat[prefix] || prefix
              return {
                id: n.id, label: n.name, subtype: nt, category: cat,
                x: n.position_x, y: n.position_y,
                config: n.config || {}, color: catColor(cat),
                schema: n.schema || null,
                configFields: n.schema?.fields ? n.schema.fields : getConfigFields(nt),
                inputs: deriveInputs(nt), outputs: deriveOutputs(nt, n.config),
                runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null,
              }
            })
            const nodeById = {}
            loadedNodes.forEach(n => { nodeById[n.id] = n })
            const loadedEdges = (wf.connections || []).map(c => ({
              id: c.id, source: c.source_node_id, sourcePortId: canonicalOutputHandle(nodeById[c.source_node_id]?.subtype, c.source_handle),
              sourcePortIdx: resolvePortIdx(nodeById[c.source_node_id]?.outputs, canonicalOutputHandle(nodeById[c.source_node_id]?.subtype, c.source_handle)),
              target: c.target_node_id, targetPortId: c.target_handle,
              targetPortIdx: resolvePortIdx(nodeById[c.target_node_id]?.inputs, c.target_handle),
            }))
            setWfId(wf.id)
            setWfName(wf.name || 'Untitled Workflow')
            setWfActive(!!wf.is_active)
            canvasIds.advancePast([...loadedNodes, ...loadedEdges].map(x => x.id))
            setNodes(loadedNodes)
            setEdges(loadedEdges)
            setSelectedId(null)
            setIsDirty(false)
            setCamera({ x: 60, y: 60, zoom: 1 })
          }
        }
        if (cancelled) return
        applyExecDetail(detail)
        const st = (detail.status || '').toUpperCase()
        if (st === 'RUNNING' || st === 'QUEUED' || st === 'PENDING' || st === 'WAITING') {
          setRunning(true)
          startExecPoll(detail.id)
        }
      } catch (e) { console.error('Failed to load execution overlay', e) }
    })()
    return () => { cancelled = true }
  }, [navData?.executionId]) // eslint-disable-line react-hooks/exhaustive-deps

  // On mount: if workflow is loaded and there's a running exec, auto-attach
  useEffect(() => {
    if (!wfId || nodes.length === 0 || running) return
    if (navData?.executionId) return
    let cancelled = false
    ;(async () => {
      try {
        const execs = await WailsApp.GetWorkflowExecutions(wfId, 3)
        if (cancelled || !execs?.length) return
        const active = execs.find(e => { const s = (e.status||'').toUpperCase(); return s==='RUNNING'||s==='QUEUED'||s==='PENDING'||s==='WAITING' })
        if (active && !cancelled) {
          setRunning(true)
          startExecPoll(active.id)
        }
      } catch (e) { console.error('Auto-discover error', e) }
    })()
    return () => { cancelled = true }
  }, [wfId, nodes.length]) // eslint-disable-line react-hooks/exhaustive-deps

  const [ghost, setGhost] = useState(null) // { template, x, y }
  const ghostRef   = useRef(null)          // same data, for mouseup handler

  const wrapperRef = useRef(null)
  const dragRef    = useRef(null)
  const nodesRef   = useRef(nodes)
  const cameraRef  = useRef(camera)
  useEffect(() => { nodesRef.current = nodes }, [nodes])
  useEffect(() => { cameraRef.current = camera }, [camera])

  // Load node types (and the installed automations, for the session picker)
  // from the backend. Reloaded when automations change elsewhere — an
  // install from the Connections page or a workflow import that installed
  // bundled packages — and on window focus as a cheap fallback.
  const [automationIds, setAutomationIds] = useState(() => new Set())
  const loadCatalog = useCallback(() => {
    api.listAutomations().then(res => { if (res && !res.error) setAutomationIds(automationIdsFrom(res)) }).catch(() => {})
    GetWorkflowNodeTypes().then(data => {
      const cats = Object.entries(data).map(([id, nodes]) => ({
        id,
        label: (paletteCategoryLabel(nodes) || id).toUpperCase(),
        section: paletteSectionOf(nodes),
        nodes: Array.isArray(nodes) ? nodes.map(n => ({
          subtype: n.type,
          label: n.label,
          category: id,
          color: catColor(id),
          inputs:  deriveInputs(n.type),
          outputs: deriveOutputs(n.type, schemaDefaults(n.schema?.fields ? n.schema.fields : getConfigFields(n.type))),
          schema: n.schema || { credential_platform: null, fields: [] },
          configFields: n.schema?.fields ? n.schema.fields : getConfigFields(n.type),
        })) : [],
      })).filter(c => c.nodes.length > 0)
      setCategories(cats)
    }).catch(() => {})
  }, [])
  useEffect(() => {
    loadCatalog()
    const off = onAutomationsChanged(loadCatalog)
    window.addEventListener('focus', loadCatalog)
    return () => { off(); window.removeEventListener('focus', loadCatalog) }
  }, [loadCatalog])

  // ── Coordinate helpers ────────────────────────────────────────────────────
  const toWorld = useCallback((cx, cy) => {
    const rect = wrapperRef.current?.getBoundingClientRect() || { left: 0, top: 0 }
    const cam  = cameraRef.current
    return { x: (cx - rect.left - cam.x) / cam.zoom, y: (cy - rect.top - cam.y) / cam.zoom }
  }, [])

  // ── Global mouse handlers ─────────────────────────────────────────────────
  useEffect(() => {
    const onMove = (e) => {
      // Ghost drag from palette
      if (ghostRef.current) {
        setGhost(g => g ? { ...g, x: e.clientX, y: e.clientY } : null)
        return
      }
      const d = dragRef.current; if (!d) return
      if (d.type === 'canvas') {
        setCamera(c => ({ ...c, x: d.camX + (e.clientX - d.startX), y: d.camY + (e.clientY - d.startY) }))
      } else if (d.type === 'node') {
        const cam = cameraRef.current
        const dx = (e.clientX - d.startX) / cam.zoom, dy = (e.clientY - d.startY) / cam.zoom
        setNodes(prev => prev.map(n => n.id === d.nodeId ? { ...n, x: d.nx + dx, y: d.ny + dy } : n))
      } else if (d.type === 'edge') {
        const w = toWorld(e.clientX, e.clientY)
        setPendingEdge(pe => pe ? { ...pe, tx: w.x, ty: w.y } : null)
      }
    }
    const onUp = (e) => {
      // Ghost drop
      if (ghostRef.current) {
        const template = ghostRef.current.template
        ghostRef.current = null
        setGhost(null)
        // Only drop if mouse released over canvas (not over palette or inspector)
        const canvasEl = wrapperRef.current
        if (canvasEl) {
          const rect = canvasEl.getBoundingClientRect()
          if (e.clientX >= rect.left && e.clientX <= rect.right &&
              e.clientY >= rect.top  && e.clientY <= rect.bottom) {
            const cam = cameraRef.current
            const wx = (e.clientX - rect.left - cam.x) / cam.zoom
            const wy = (e.clientY - rect.top  - cam.y) / cam.zoom
            // Use functional addNode by calling it after render via timeout
            addNodeRef.current?.(template, wx, wy)
          }
        }
        return
      }
      if (dragRef.current?.type === 'edge') setPendingEdge(null)
      dragRef.current = null
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    return () => { document.removeEventListener('mousemove', onMove); document.removeEventListener('mouseup', onUp) }
  }, [toWorld])

  // ── Scroll to zoom ────────────────────────────────────────────────────────
  useEffect(() => {
    const el = wrapperRef.current; if (!el) return
    const onWheel = (e) => {
      e.preventDefault()
      const factor = e.deltaY < 0 ? 1.1 : 0.9
      setCamera(c => {
        const z = Math.max(0.25, Math.min(2.5, c.zoom * factor))
        const rect = el.getBoundingClientRect()
        const mx = e.clientX - rect.left, my = e.clientY - rect.top
        return { x: mx - (mx - c.x) * (z / c.zoom), y: my - (my - c.y) * (z / c.zoom), zoom: z }
      })
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  // ── Delete selected node ──────────────────────────────────────────────────
  useEffect(() => {
    const onKey = (e) => {
      if ((e.key === 'Delete' || e.key === 'Backspace') && !['INPUT','TEXTAREA','SELECT'].includes(e.target.tagName)) {
        if (selectedId) deleteNode(selectedId)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selectedId]) // eslint-disable-line

  // ── Add node ──────────────────────────────────────────────────────────────
  const addNode = useCallback((template, worldX, worldY) => {
    const cam = cameraRef.current
    const rect = wrapperRef.current?.getBoundingClientRect() || { width: 900, height: 600 }
    const x = worldX ?? (rect.width / 2 - cam.x) / cam.zoom - NODE_W / 2 + (Math.random() - .5) * 100
    const y = worldY ?? (rect.height / 2 - cam.y) / cam.zoom - 60 + (Math.random() - .5) * 80
    const defaults = {}
    template.configFields?.forEach(f => { defaults[f.key] = f.default ?? '' })
    const id = uid(new Set(nodesRef.current.map(n => n.id)))
    setNodes(prev => [...prev, {
      id, label: template.label, subtype: template.subtype,
      category: template.category,
      color: template.color || catColor(template.category || template.id),
      inputs:  template.inputs  || [],
      outputs: template.subtype ? deriveOutputs(template.subtype, defaults) : (template.outputs || []),
      schema: template.schema || { credential_platform: null, fields: [] },
      configFields: template.configFields || [],
      config: defaults,
      x, y,
      runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null,
    }])
    setSelectedId(id)
    setIsDirty(true)
  }, [])

  // Keep addNode accessible from mouseup closure via ref
  const addNodeRef = useRef(null)
  useEffect(() => { addNodeRef.current = addNode }, [addNode])

  // ── Palette ghost drag start ──────────────────────────────────────────────
  const onPaletteNodeMouseDown = useCallback((e, template) => {
    e.preventDefault()
    e.stopPropagation()
    ghostRef.current = { template }
    setGhost({ template, x: e.clientX, y: e.clientY })
  }, [])

  const deleteNode = (id) => {
    setNodes(prev => prev.filter(n => n.id !== id))
    setEdges(prev => prev.filter(e => e.source !== id && e.target !== id))
    if (id === selectedId) { setSelectedId(null); setInspectorOpen(false) }
    setIsDirty(true)
  }

  const updateConfig = (nodeId, key, val) => {
    const node = nodesRef.current.find(n => n.id === nodeId)
    // Switch / ai.choose ports follow their cases: re-derive them and re-point
    // (or drop) this node's outgoing edges by handle id.
    const portsChange = node && portsDependOnConfig(node.subtype, key) && isCaseListSettled(val)
    const outputs = portsChange ? deriveOutputs(node.subtype, { ...node.config, [key]: val }) : null
    setNodes(prev => prev.map(n => n.id === nodeId
      ? { ...n, config: { ...n.config, [key]: val }, ...(outputs ? { outputs } : {}) }
      : n))
    if (outputs) setEdges(prev => remapSourceEdges(prev, nodeId, outputs, node.outputs))
    setIsDirty(true)
  }

  // ── Edge drawing ──────────────────────────────────────────────────────────
  const startEdge = (e, nodeId, portIdx) => {
    const node = nodesRef.current.find(n => n.id === nodeId); if (!node) return
    const pos = outPortPos(node, portIdx)
    dragRef.current = { type: 'edge', sourceNodeId: nodeId, sourcePortIdx: portIdx }
    setPendingEdge({ sx: pos.x, sy: pos.y, tx: pos.x, ty: pos.y })
  }

  const completeEdge = (targetNodeId, targetPortIdx) => {
    if (!dragRef.current || dragRef.current.type !== 'edge') return
    const { sourceNodeId, sourcePortIdx } = dragRef.current
    if (sourceNodeId === targetNodeId) { dragRef.current = null; setPendingEdge(null); return }
    const sNode = nodesRef.current.find(n => n.id === sourceNodeId)
    const tNode = nodesRef.current.find(n => n.id === targetNodeId)
    if (!sNode || !tNode) { dragRef.current = null; setPendingEdge(null); return }
    // A cycle is allowed only as a loop closing into a Split In Batches node.
    const candidate = { source: sourceNodeId, sourcePortIdx, sourcePortId: sNode.outputs[sourcePortIdx]?.id, target: targetNodeId }
    if (hasForbiddenCycle(nodesRef.current, [...edges, candidate])) {
      notify('connect nodes', 'Connection rejected: it would create a cycle. Only a loop back into a Split In Batches node may close one.')
      dragRef.current = null; setPendingEdge(null)
      return
    }
    setEdges(prev => {
      if (prev.some(e => e.source === sourceNodeId && e.sourcePortIdx === sourcePortIdx && e.target === targetNodeId && e.targetPortIdx === targetPortIdx)) return prev
      return [...prev, {
        id: uid(new Set(prev.map(e => e.id))), source: sourceNodeId, sourcePortIdx,
        sourcePortId: sNode.outputs[sourcePortIdx]?.id,
        target: targetNodeId, targetPortIdx,
        targetPortId: tNode.inputs[targetPortIdx]?.id,
      }]
    })
    setIsDirty(true)
    dragRef.current = null; setPendingEdge(null)
  }

  // ── RUN ──────────────────────────────────────────────────────────────────
  const handleStop = useCallback(async () => {
    const execId = execOverlay?.id
    stopPolling()
    setGlobalStatus('error')
    if (execId) {
      try { await api.cancelWorkflow(execId) } catch {}
    }
  }, [execOverlay])

  // Per-run token: a stale exec-started listener from an earlier run must
  // never resolve the current one.
  const runTokenRef = useRef(0)

  // A workflow whose nodes read {{ $json.<field> }} needs trigger data; ask
  // for it once, then run. Without this the run button sent nothing and such
  // a workflow completed having done nothing at all.
  //
  // What it reads is the CLI's answer (`workflow inputs --json`), not
  // something derived here — the dialog only renders it. A workflow that has
  // never been saved has no id to ask about, and an unreachable CLI must not
  // block a run, so both fall through to running with no input.
  const handleRun = async () => {
    if (running || nodes.length === 0) return
    if (triggerInput.asked) return // dialog already open; it drives the run
    if (wfId && !isDirty) {
      const inputs = await api.getWorkflowTriggerInputs(wfId)
      if (inputs?.fields?.length > 0) {
        setTriggerInput({
          asked: true,
          fields: inputs.fields,
          value: rememberedTriggerInput(wfId, inputs.skeleton),
        })
        return
      }
    }
    await startRun('')
  }

  const startRun = async (inputJSON) => {
    if (running || nodes.length === 0) return
    stopRef.current = false
    setRunning(true)
    setGlobalStatus(null)
    setExecOverlay(null)
    setProblemHidden(false)
    setNodes(prev => prev.map(n => ({ ...n, runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null })))
    if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }

    const runToken = ++runTokenRef.current

    try {
      // Unsaved workflow (no id yet) or dirty canvas: save first, then run.
      let currentWfId = wfId
      if (!wfId || isDirty) {
        const saved = await handleSave()
        if (!saved?.id) {
          notify('run workflow', 'Auto-save failed — workflow not run')
          setRunning(false)
          return
        }
        currentWfId = saved.id
      }

      // Listen for the execution ID via Wails event — emitted when CLI prints
      // "Execution started: <id>". subscribeEvent returns a cancel fn that
      // removes only this listener (EventsOff would kill all of them).
      // A process that exits before naming an execution (workflow:complete
      // first) resolves null at once instead of waiting out the timeout.
      let offEvent = () => {}
      let offComplete = () => {}
      let cancelTimeout = () => {}
      const execIdPromise = new Promise((resolve) => {
        offEvent = subscribeEvent('workflow:exec-started', (data) => {
          if (runTokenRef.current !== runToken) return // stale run's listener
          if (data?.execution_id) resolve(data.execution_id)
        })
        offComplete = subscribeEvent('workflow:complete', (data) => {
          if (runTokenRef.current !== runToken) return
          if (data?.workflow_id === currentWfId) resolve(null)
        })
        const t = setTimeout(() => resolve(null), 60000)
        cancelTimeout = () => clearTimeout(t)
      })

      try {
        // A refused run (inactive, already running, bad input) answers at
        // once: reset the button and say why instead of waiting for an
        // execution that will never start.
        const started = await api.runWorkflowWithInput(currentWfId, inputJSON)
        if (!started?.ok) {
          if (runTokenRef.current === runToken) setRunning(false)
          reportRunFailure(started?.error || 'Run failed', {
            activateAndRun: () => activateAndRun(currentWfId, inputJSON),
          })
          return
        }
        rememberTriggerInput(currentWfId, inputJSON)
        const execId = await execIdPromise
        if (runTokenRef.current !== runToken) return // superseded by a newer run
        if (!execId) {
          setRunning(false)
          notify('run workflow', 'The run stopped before it started — open Live Logs for the output.')
          return
        }

        // Start polling execution detail — simple setInterval, no React effects
        startExecPoll(execId)
      } finally {
        cancelTimeout()
        offEvent()
        offComplete()
      }
    } catch (err) {
      console.error('Run failed', err)
      setRunning(false)
    }
  }
  const startRunRef = useRef(startRun)
  startRunRef.current = startRun

  // The "Activate & run" toast action: the user's click switches the
  // workflow on, then the same run starts again.
  const activateAndRun = async (id, inputJSON) => {
    try {
      await SetWorkflowActive(id, true)
    } catch (e) {
      notify('activate workflow', e?.message || e)
      return
    }
    setWfActive(true)
    startRunRef.current(inputJSON)
  }

  // ── Save workflow ─────────────────────────────────────────────────────────
  const handleSave = useCallback(async (nameOverride) => {
    if (saving) return
    setSaving(true)
    setShowSaveModal(false)
    const finalName = nameOverride || wfName || 'Untitled Workflow'
    if (nameOverride) setWfName(nameOverride)
    try {
      const req = {
        id: wfId || '',
        name: finalName,
        description: '',
        nodes: nodes.map(n => ({
          id: n.id,
          node_type: n.subtype,
          name: n.label,
          config: normalizeConfigForSave(n.subtype, n.config),
          position_x: n.x,
          position_y: n.y,
          disabled: false,
        })),
        connections: edges.map((e, i) => ({
          id: e.id,
          source_node_id: e.source,
          source_handle: e.sourcePortId || String(e.sourcePortIdx ?? 0),
          target_node_id: e.target,
          target_handle: e.targetPortId || String(e.targetPortIdx ?? 0),
          position: i,
        })),
      }
      const saved = await SaveWorkflow(req)
      if (saved?.id) {
        setWfId(saved.id)
        setIsDirty(false)
        onWorkflowsChanged?.() // status bar / dashboard workflow count
        setSaveMsg({ ok: true, text: 'Saved' })
        return saved
      } else {
        setSaveMsg({ ok: false, text: 'Save returned no ID' })
        return null
      }
    } catch (e) {
      setSaveMsg({ ok: false, text: String(e) })
      return null
    } finally {
      setSaving(false)
      setTimeout(() => setSaveMsg(null), 3000)
    }
  }, [saving, wfId, wfName, nodes, edges, setShowSaveModal])

  // ── Load workflow ─────────────────────────────────────────────────────────
  // keepModal: the mount-time restore must not close a Templates dialog the
  // dashboard just asked for.
  const handleLoad = useCallback(async (id, { keepModal = false } = {}) => {
    // Guard the dirty canvas: Load (modal), template creation and AI-created
    // workflows all funnel through here.
    if (isDirty && !(await confirm('Discard unsaved changes?', { title: 'Load Workflow', confirmLabel: 'Discard & Load' }))) return
    stopPolling() // a poll from a previous run must not repaint the new canvas
    try {
      const wf = await GetWorkflow(id)
      if (!wf) return
      setWfId(wf.id)
      setWfName(wf.name || 'Untitled Workflow')
      setWfActive(!!wf.is_active)
      // Map backend WorkflowNodeData → canvas node shape
      const prefixToCat = { core: 'control', db: 'database', comm: 'communication', service: 'services', data: 'data', http: 'http', system: 'system', trigger: 'triggers', ai: 'ai', instagram: 'instagram', linkedin: 'linkedin', x: 'x', tiktok: 'tiktok' }
      const loadedNodes = (wf.nodes || []).map(n => {
        const nt = normalizeNodeType(n.node_type || n.type || '')
        const prefix = nt.split('.')[0]
        const cat = prefixToCat[prefix] || prefix
        return {
        id:       n.id,
        label:    n.name,
        subtype:  nt,
        category: cat,
        x: n.position_x,
        y: n.position_y,
        config: n.config || {},
        color: catColor(cat),
        schema: n.schema || null,
        configFields: n.schema?.fields ? n.schema.fields : getConfigFields(nt),
        inputs:  deriveInputs(nt),
        outputs: deriveOutputs(nt, n.config),
        runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null,
      }})
      // Map backend WorkflowConnectionData → canvas edge shape. Handles are
      // port ids ('main', 'true', …) — resolve to indexes; legacy numeric
      // strings fall back to the parsed index.
      const nodeById = {}
      loadedNodes.forEach(n => { nodeById[n.id] = n })
      const loadedEdges = (wf.connections || []).map(c => ({
        id:           c.id,
        source:       c.source_node_id,
        sourcePortId: canonicalOutputHandle(nodeById[c.source_node_id]?.subtype, c.source_handle),
        sourcePortIdx: resolvePortIdx(nodeById[c.source_node_id]?.outputs, canonicalOutputHandle(nodeById[c.source_node_id]?.subtype, c.source_handle)),
        target:       c.target_node_id,
        targetPortId: c.target_handle,
        targetPortIdx: resolvePortIdx(nodeById[c.target_node_id]?.inputs, c.target_handle),
      }))
      canvasIds.advancePast([...loadedNodes, ...loadedEdges].map(x => x.id))
      setNodes(loadedNodes)
      setEdges(loadedEdges)
      setSelectedId(null)
      setGlobalStatus(null)
      setIsDirty(false)
      setCamera({ x: 60, y: 60, zoom: 1 })
      if (!keepModal) setShowWfModal(false)
    } catch (e) {
      console.error('Load failed', e)
    }
  }, [isDirty]) // eslint-disable-line react-hooks/exhaustive-deps

  // ── Auto-load last workflow on mount ──────────────────────────────────────
  useEffect(() => {
    if (wfId && nodes.length === 0) handleLoad(wfId, { keepModal: true })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // ── Refresh: force-reload the currently open workflow from the backend ────
  const [refreshing, setRefreshing] = useState(false)
  const handleRefresh = useCallback(async () => {
    if (!wfId) return
    setRefreshing(true)
    try { await handleLoad(wfId) } finally { setRefreshing(false) }
  }, [wfId, handleLoad])

  // ── Toggle active ─────────────────────────────────────────────────────────
  const handleToggleActive = useCallback(async () => {
    const next = !wfActive
    setWfActive(next)
    if (wfId) { try { await SetWorkflowActive(wfId, next) } catch {} }
  }, [wfActive, wfId])

  // ── New canvas ────────────────────────────────────────────────────────────
  const resetCanvas = useCallback(() => {
    setWfId(null); setWfName('Untitled Workflow'); setWfActive(false)
    setNodes([]); setEdges([]); setSelectedId(null); setGlobalStatus(null)
    setIsDirty(false); setCamera({ x: 60, y: 60, zoom: 1 })
    setExecOverlay(null)
  }, [])

  const handleNew = useCallback(async () => {
    if (isDirty && nodes.length > 0) {
      if (!(await confirm('Create a new workflow? Unsaved changes will be lost.', { title: 'New Workflow', confirmLabel: 'Discard & New', danger: false }))) return
    }
    stopPolling()
    resetCanvas()
  }, [nodes.length, isDirty, resetCanvas]) // eslint-disable-line react-hooks/exhaustive-deps

  // ── Auto-layout: topological left-to-right layout ─────────────────────────
  const handleAutoLayout = useCallback(() => {
    if (nodes.length === 0) return
    const NODE_W = 180, NODE_H = 80, GAP_X = 80, GAP_Y = 60
    // Build adjacency: nodeId → list of successor nodeIds
    const successors = {}
    const predecessors = {}
    nodes.forEach(n => { successors[n.id] = []; predecessors[n.id] = [] })
    // Loop back-edges point backwards; following them would never terminate.
    const back = loopBackEdges(nodes, edges)
    edges.forEach(e => {
      if (back.has(e)) return
      if (successors[e.source]) successors[e.source].push(e.target)
      if (predecessors[e.target]) predecessors[e.target].push(e.source)
    })
    // Assign column (depth) via BFS from roots
    const col = {}
    const roots = nodes.filter(n => predecessors[n.id].length === 0).map(n => n.id)
    const queue = roots.map(id => ({ id, depth: 0 }))
    while (queue.length) {
      const { id, depth } = queue.shift()
      if (col[id] !== undefined && col[id] >= depth) continue
      col[id] = depth
      successors[id].forEach(sid => queue.push({ id: sid, depth: depth + 1 }))
    }
    // Nodes with no column (disconnected) go at the end
    nodes.forEach(n => { if (col[n.id] === undefined) col[n.id] = 0 })
    // Group nodes by column, assign row within column
    const byCol = {}
    nodes.forEach(n => {
      const c = col[n.id] || 0
      if (!byCol[c]) byCol[c] = []
      byCol[c].push(n.id)
    })
    // Calculate positions
    const positions = {}
    Object.keys(byCol).forEach(c => {
      const colNodes = byCol[c]
      colNodes.forEach((id, row) => {
        positions[id] = {
          x: 60 + parseInt(c) * (NODE_W + GAP_X),
          y: 60 + row * (NODE_H + GAP_Y),
        }
      })
    })
    setNodes(prev => prev.map(n => ({ ...n, x: positions[n.id]?.x ?? n.x, y: positions[n.id]?.y ?? n.y })))
    setCamera({ x: 40, y: 40, zoom: 1 })
  }, [nodes, edges])

  // ── Edge paths ────────────────────────────────────────────────────────────
  // Loop back-edges run below (or above) their loop's nodes, dashed in
  // LOOP_EDGE_COLOR.
  const backEdges = loopBackEdges(nodes, edges)
  const edgePaths = edges.map(edge => {
    const sNode = nodes.find(n => n.id === edge.source)
    const tNode = nodes.find(n => n.id === edge.target)
    if (!sNode || !tNode) return null
    const sp = outPortPos(sNode, edge.sourcePortIdx)
    const tp = inPortPos(tNode, edge.targetPortIdx)
    const loopIds = backEdges.get(edge)
    if (loopIds) {
      const boxes = nodes.map(n => ({ id: n.id, x: n.x, y: n.y, w: NODE_W, h: nodeH(n) }))
      // Other edges' curves and the output ports of nodes outside the loop:
      // the route (below or above the loop) keeps clear of them.
      const obstacles = []
      for (const o of edges) {
        if (o === edge) continue
        const os = nodes.find(n => n.id === o.source), ot = nodes.find(n => n.id === o.target)
        if (!os || !ot) continue
        const a = outPortPos(os, o.sourcePortIdx), b = inPortPos(ot, o.targetPortIdx)
        obstacles.push(...edgePoints(a.x, a.y, b.x, b.y))
      }
      for (const n of nodes) if (!loopIds.has(n.id)) n.outputs.forEach((_, i) => { const p = outPortPos(n, i); obstacles.push([p.x, p.y]) })
      const runY = chooseLoopEdgeY({ sx: sp.x, sy: sp.y, tx: tp.x, ty: tp.y, boxes, loopIds, obstacles })
      return { ...edge, path: loopEdgePath(sp.x, sp.y, tp.x, tp.y, runY), color: LOOP_EDGE_COLOR, loop: true }
    }
    return { ...edge, path: edgePath(sp.x, sp.y, tp.x, tp.y), color: sNode.color || catColor(sNode.category) }
  }).filter(Boolean)

  const pendingPath = pendingEdge ? edgePath(pendingEdge.sx, pendingEdge.sy, pendingEdge.tx, pendingEdge.ty) : null
  const selectedNode = nodes.find(n => n.id === selectedId) || null
  const canRun = nodes.length > 0 && !running

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', background: '#04060a', overflow: 'hidden' }}>
      {/* ── TOOLBAR ── */}
      <div style={{
        height: 44, flexShrink: 0,
        display: 'flex', alignItems: 'center', gap: 6,
        padding: '0 12px',
        background: '#080d16',
        borderBottom: '1px solid rgba(0,180,216,0.1)',
        zIndex: 10,
      }}>
        {/* Workflow name */}
        <input
          value={wfName}
          onChange={e => { setWfName(e.target.value); setIsDirty(true) }}
          style={{
            background: 'transparent', border: 'none', outline: 'none',
            fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700,
            color: 'var(--text-secondary)', letterSpacing: 1,
            width: 200, minWidth: 80,
          }}
        />
        {isDirty && (
          <span style={{
            fontFamily: 'var(--font-mono)', fontSize: 9, letterSpacing: 1,
            color: '#f59e0b', display: 'flex', alignItems: 'center', gap: 3,
          }}>
            <span style={{ width: 6, height: 6, borderRadius: '50%', background: '#f59e0b', display: 'inline-block' }} />
            UNSAVED
          </span>
        )}

        {/* Active toggle */}
        <button
          onClick={handleToggleActive}
          title={wfActive ? 'Deactivate' : 'Activate'}
          style={{ background: 'transparent', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4, color: wfActive ? '#10b981' : 'var(--text-muted)', padding: '2px 4px' }}
        >
          {wfActive ? <ToggleRight size={16} /> : <ToggleLeft size={16} />}
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, letterSpacing: 1 }}>{wfActive ? 'ACTIVE' : 'OFF'}</span>
        </button>

        <div style={{ width: 1, height: 16, background: 'rgba(0,180,216,0.15)' }} />

        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>
          {nodes.length}n · {edges.length}e
        </span>

        {saveMsg && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            {saveMsg.ok
              ? <CheckCircle size={11} color="#10b981" />
              : <AlertCircle size={11} color="#ef4444" />}
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: saveMsg.ok ? '#10b981' : '#ef4444', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {saveMsg.text}
            </span>
          </div>
        )}

        {globalStatus && !saveMsg && !execOverlay && (() => {
          const g = globalStatus === 'ok'
            ? { color: '#10b981', label: 'done', Icon: CheckCircle }
            : globalStatus === 'warning'
              ? { color: '#fbbf24', label: 'done with errors', Icon: AlertCircle }
              : globalStatus === 'waiting'
                ? { color: '#f59e0b', label: 'waiting for review', Icon: Clock }
                : { color: '#ef4444', label: 'failed', Icon: AlertCircle }
          return (
            <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <g.Icon size={11} color={g.color} />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: g.color }}>{g.label}</span>
            </div>
          )
        })()}

        {/* Execution overlay banner */}
        {execOverlay && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 6,
            padding: '3px 10px',
            background: execOverlay.status === 'RUNNING' ? 'rgba(0,180,216,0.1)' :
                        execOverlay.status === 'WAITING' ? 'rgba(245,158,11,0.1)' :
                        execOverlay.status === 'COMPLETED' || execOverlay.status === 'SUCCESS' ? 'rgba(16,185,129,0.1)' :
                        execOverlay.status === 'FAILED' ? 'rgba(239,68,68,0.1)' : 'rgba(107,114,128,0.1)',
            border: `1px solid ${
              execOverlay.status === 'RUNNING' ? 'rgba(0,180,216,0.3)' :
              execOverlay.status === 'WAITING' ? 'rgba(245,158,11,0.3)' :
              execOverlay.status === 'COMPLETED' || execOverlay.status === 'SUCCESS' ? 'rgba(16,185,129,0.3)' :
              execOverlay.status === 'FAILED' ? 'rgba(239,68,68,0.3)' : 'rgba(107,114,128,0.3)'}`,
            borderRadius: 6,
          }}>
            {execOverlay.status === 'RUNNING' && <Loader size={10} style={{ animation: 'spin 1s linear infinite', color: 'var(--cyan)' }} />}
            {execOverlay.status === 'WAITING' && <Clock size={10} color="#f59e0b" />}
            {(execOverlay.status === 'COMPLETED' || execOverlay.status === 'SUCCESS') && <CheckCircle size={10} color="#10b981" />}
            {execOverlay.status === 'FAILED' && <AlertCircle size={10} color="#ef4444" />}
            {execOverlay.status === 'CANCELLED' && <X size={10} color="#6b7280" />}
            {execOverlay.status === 'SUCCESS_WITH_ERRORS' && <AlertCircle size={10} color="#fbbf24" />}
            <span
              role={execOverlay.problem ? 'button' : undefined}
              tabIndex={execOverlay.problem ? 0 : undefined}
              onClick={execOverlay.problem ? () => setProblemHidden(h => !h) : undefined}
              onKeyDown={execOverlay.problem ? (e) => { if (e.key === 'Enter' || e.key === ' ') setProblemHidden(h => !h) } : undefined}
              title={execOverlay.problem ? (problemHidden ? 'Show why' : 'Hide the error') : undefined}
              style={{
                fontFamily: 'var(--font-mono)', fontSize: 9,
                color: execOverlay.status === 'RUNNING' ? 'var(--cyan)' :
                       execOverlay.status === 'WAITING' ? '#f59e0b' :
                       execOverlay.status === 'COMPLETED' || execOverlay.status === 'SUCCESS' ? '#10b981' :
                       execOverlay.status === 'FAILED' ? '#ef4444' :
                       execOverlay.status === 'SUCCESS_WITH_ERRORS' ? '#fbbf24' : '#6b7280',
                textTransform: 'uppercase',
                cursor: execOverlay.problem ? 'pointer' : 'default',
                textDecoration: execOverlay.problem && problemHidden ? 'underline' : 'none',
              }}>
              Execution: {execOverlay.status}
            </span>
            {execOverlay.status === 'FAILED' && isAgentNotSetup(execOverlay.error) && <AgentSetupLink onNavigate={onNavigate} compact />}
            <button
              style={{
                background: 'none', border: 'none', cursor: 'pointer', padding: 2,
                color: 'var(--text-muted)', display: 'flex', alignItems: 'center',
              }}
              onClick={() => {
                if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
                setExecOverlay(null)
                setNodes(prev => prev.map(n => ({ ...n, runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null })))
              }}
              title="Close execution view"
            >
              <X size={10} />
            </button>
          </div>
        )}

        {/* HIL pause hint — WAITING runs wait for a human, they did not fail */}
        {execOverlay?.status === 'WAITING' && (
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: '#f59e0b', display: 'flex', alignItems: 'center', gap: 5, whiteSpace: 'nowrap' }}>
            {execOverlay.hint || 'Paused for human review'}
            <button
              onClick={() => onNavigate?.('hil')}
              style={{ background: 'transparent', border: 'none', cursor: 'pointer', padding: 0, fontFamily: 'var(--font-mono)', fontSize: 9, color: '#00b4d8', textDecoration: 'underline' }}
              title="Open Human in Loop page"
            >
              Human in Loop →
            </button>
          </span>
        )}

        <div style={{ flex: 1 }} />

        {/* New */}
        <button style={tbBtn} onClick={handleNew} title="New workflow"><Plus size={13} /></button>

        {/* Load */}
        <button style={tbBtn} onClick={() => setShowWfModal('saved')} title="Open saved workflow"><FolderOpen size={13} /></button>

        {/* Templates: labelled, so starting from one needs no hunting */}
        <button style={{ ...tbBtn, gap: 5, fontFamily: 'var(--font-mono)', fontSize: 10 }} onClick={() => setShowWfModal('templates')} title="Start from a template">
          <LayoutTemplate size={13} /> Templates
        </button>

        {/* Import a workflow file (CLI `workflow import`) */}
        <button style={tbBtn} onClick={() => setShowImport(true)} title="Import workflow" aria-label="Import workflow"><Upload size={13} /></button>

        {/* Publish the saved workflow to monoes.me (CLI `library publish`) */}
        <button
          style={{ ...tbBtn, opacity: wfId ? 1 : 0.4 }}
          onClick={() => setShowPublish(true)}
          disabled={!wfId}
          title={wfId ? 'Publish to monoes' : 'Save the workflow first'}
          aria-label="Publish to monoes"
        ><UploadCloud size={13} /></button>

        {/* Refresh: reload the open workflow fresh from the backend, discarding unsaved canvas edits */}
        <button
          style={{ ...tbBtn, opacity: wfId ? 1 : 0.4 }}
          onClick={handleRefresh}
          disabled={!wfId || refreshing}
          title={wfId ? 'Reload workflow from disk' : 'No workflow open'}
        >
          <RefreshCw size={13} style={{ animation: refreshing ? 'spin 0.7s linear infinite' : 'none' }} />
        </button>

        {/* Save */}
        <button
          style={{ ...tbBtn, color: saving ? 'var(--text-muted)' : '#00b4d8', borderColor: 'rgba(0,180,216,0.3)' }}
          onClick={() => { if (!saving) setShowSaveModal(true) }}
          title="Save workflow"
        >
          {saving ? <Loader size={12} style={{ animation: 'spin 0.7s linear infinite' }} /> : <Save size={13} />}
        </button>

        <div style={{ width: 1, height: 16, background: 'rgba(0,180,216,0.15)' }} />

        {/* Zoom */}
        <button style={tbBtn} onClick={() => setCamera(c => ({ ...c, zoom: Math.max(0.25, c.zoom / 1.2) }))} title="Zoom out"><ZoomOut size={13} /></button>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', minWidth: 32, textAlign: 'center' }}>
          {Math.round(camera.zoom * 100)}%
        </span>
        <button style={tbBtn} onClick={() => setCamera(c => ({ ...c, zoom: Math.min(2.5, c.zoom * 1.2) }))} title="Zoom in"><ZoomIn size={13} /></button>
        <button style={tbBtn} onClick={() => setCamera({ x: 60, y: 60, zoom: 1 })} title="Reset view"><RotateCcw size={13} /></button>
        <button style={tbBtn} onClick={handleAutoLayout} title="Auto-layout nodes"><LayoutDashboard size={13} /></button>

        {/* Clear */}
        <button
          style={{ ...tbBtn, color: nodes.length ? 'rgba(239,68,68,0.6)' : 'var(--text-muted)' }}
          onClick={async () => { if (nodes.length > 0 && !(await confirm('Clear the entire canvas?', { title: 'Clear Canvas', confirmLabel: 'Clear' }))) return; setNodes([]); setEdges([]); setSelectedId(null); setGlobalStatus(null) }}
          title="Clear canvas"
          aria-label="Clear canvas"
        >
          <Trash2 size={13} />
        </button>

        {/* JSON view toggle */}
        <button
          style={{ ...tbBtn, color: jsonView ? '#00b4d8' : 'var(--text-muted)', borderColor: jsonView ? 'rgba(0,180,216,0.3)' : 'rgba(0,180,216,0.15)', background: jsonView ? 'rgba(0,180,216,0.08)' : 'transparent' }}
          onClick={() => setJsonView(v => !v)}
          title={jsonView ? 'Switch to visual canvas' : 'Switch to JSON view'}
        >
          <Braces size={13} />
        </button>

        {/* Run / Stop */}
        {running ? (
          <button
            onClick={handleStop}
            style={{
              ...tbBtn,
              background: 'rgba(239,68,68,0.12)',
              border: '1px solid rgba(239,68,68,0.35)',
              color: '#ef4444',
              padding: '5px 14px', gap: 5,
            }}
            onMouseEnter={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.22)' }}
            onMouseLeave={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.12)' }}
            title="Stop execution"
          >
            <Square size={12} />
            STOP
          </button>
        ) : (
          <button
            onClick={handleRun}
            disabled={nodes.length === 0}
            style={{
              ...tbBtn,
              background: nodes.length > 0 ? 'rgba(16,185,129,0.12)' : 'rgba(100,116,139,0.06)',
              border: `1px solid ${nodes.length > 0 ? 'rgba(16,185,129,0.3)' : 'rgba(100,116,139,0.1)'}`,
              color: nodes.length > 0 ? '#10b981' : 'var(--text-muted)',
              padding: '5px 14px', gap: 5,
              opacity: nodes.length > 0 ? 1 : 0.5,
            }}
            onMouseEnter={e => { if (nodes.length > 0) e.currentTarget.style.background = 'rgba(16,185,129,0.2)' }}
            onMouseLeave={e => { if (nodes.length > 0) e.currentTarget.style.background = 'rgba(16,185,129,0.12)' }}
            title="Run all nodes"
          >
            <Play size={12} />
            RUN
          </button>
        )}
      </div>

      {/* ── SAVE MODAL ── */}
      {showSaveModal && (
        <SaveModal
          initialName={wfName}
          onConfirm={handleSave}
          onClose={() => setShowSaveModal(false)}
        />
      )}

      {/* ── TRIGGER INPUT MODAL ── */}
      {triggerInput.asked && triggerInput.fields.length > 0 && (
        <TriggerInputModal
          fields={triggerInput.fields}
          initialValue={triggerInput.value}
          onRun={(value) => {
            setTriggerInput({ asked: false, fields: [], value: '' })
            startRun(value)
          }}
          onClose={() => setTriggerInput({ asked: false, fields: [], value: '' })}
        />
      )}

      {showPublish && wfId && (
        <PublishToMonoesDialog kind="workflow" localId={wfId} defaultName={wfName} onClose={() => setShowPublish(false)} />
      )}

      {/* ── WORKFLOWS MODAL ── */}
      {showImport && (
        <WorkflowImportDialog
          onClose={() => setShowImport(false)}
          onImported={() => onWorkflowsChanged?.()}
          onAutomationsInstalled={loadCatalog}
          onOpen={(id) => { setShowImport(false); handleLoad(id) }}
        />
      )}

      {showWfModal && (
        <WorkflowsModal
          initialTab={showWfModal === 'templates' ? 'templates' : 'saved'}
          currentId={wfId}
          onLoad={handleLoad}
          onLibraryInstalled={() => { onWorkflowsChanged?.(); loadCatalog() }}
          onDelete={async (id) => {
            if (!(await confirm('Delete this workflow? This cannot be undone.', { title: 'Delete Workflow', confirmLabel: 'Delete' }))) return false
            await DeleteWorkflow(id)
            onWorkflowsChanged?.()
            if (id === wfId) {
              stopPolling()
              resetCanvas()
            }
            return true
          }}
          onClose={() => setShowWfModal(false)}
        />
      )}

      {execOverlay?.problem && !problemHidden && (
        <RunProblemPanel
          problem={execOverlay.problem}
          onSelectNode={(id) => { setSelectedId(id); setInspectorOpen(true) }}
          onClose={() => setProblemHidden(true)}
        />
      )}

      {/* ── MAIN LAYOUT ── */}
      <div style={{ display: 'flex', flex: 1, overflow: 'hidden' }}>

        {/* Palette */}
        <Palette categories={categories} onAdd={addNode} onNodeMouseDown={onPaletteNodeMouseDown}
          onBrowseAutomations={() => onNavigate?.('connections')} />

        {/* Canvas / JSON view */}
        {jsonView ? (
          <div style={{
            flex: 1, position: 'relative', overflow: 'auto',
            background: '#04060a',
            padding: 16,
          }}>
            <div style={{
              fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)',
              letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 10,
              display: 'flex', alignItems: 'center', gap: 6,
            }}>
              <Braces size={11} color="#00b4d8" />
              WORKFLOW JSON
              {wfId && <span style={{ opacity: 0.5 }}>· {wfId}</span>}
            </div>
            <pre style={{
              margin: 0,
              fontFamily: 'var(--font-mono)', fontSize: 11,
              color: '#e2e8f0', lineHeight: 1.6,
              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              background: '#020509',
              border: '1px solid rgba(0,180,216,0.1)',
              borderRadius: 8,
              padding: 16,
              minHeight: 200,
            }}>
              {JSON.stringify({
                id: wfId || null,
                name: wfName,
                nodes: nodes.map(n => ({
                  id: n.id,
                  node_type: n.subtype,
                  name: n.label,
                  config: normalizeConfigForSave(n.subtype, n.config),
                  position_x: Math.round(n.x),
                  position_y: Math.round(n.y),
                })),
                connections: edges.map((e, i) => ({
                  id: e.id,
                  source_node_id: e.source,
                  source_handle: e.sourcePortId || String(e.sourcePortIdx ?? 0),
                  target_node_id: e.target,
                  target_handle: e.targetPortId || String(e.targetPortIdx ?? 0),
                })),
              }, null, 2)}
            </pre>
          </div>
        ) : (
          <div
            ref={wrapperRef}
            style={{ flex: 1, position: 'relative', overflow: 'hidden', cursor: 'default' }}
            onMouseDown={(e) => {
              if (e.target !== wrapperRef.current && !e.target.dataset.bg) return
              setSelectedId(null)
              dragRef.current = { type: 'canvas', startX: e.clientX, startY: e.clientY, camX: cameraRef.current.x, camY: cameraRef.current.y }
            }}
          >
            {/* Dot grid */}
            <div data-bg="1" style={{
              position: 'absolute', inset: 0,
              backgroundImage: 'radial-gradient(circle,rgba(0,180,216,0.18) 1.2px,transparent 1.2px)',
              backgroundSize: '28px 28px',
              backgroundPosition: `${camera.x % 28}px ${camera.y % 28}px`,
              pointerEvents: 'none',
            }} />

            {/* Empty state */}
            {nodes.length === 0 && (
              <div style={{
                position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column',
                alignItems: 'center', justifyContent: 'center', gap: 12,
                pointerEvents: 'none',
              }}>
                <Plus size={32} style={{ opacity: 0.1 }} />
                <button className="btn btn-secondary btn-sm" style={{ pointerEvents: 'auto', gap: 6 }} onClick={() => setShowWfModal('templates')}>
                  <LayoutTemplate size={13} /> Start from a template
                </button>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--text-muted)', textAlign: 'center', lineHeight: 1.8 }}>
                  or click or drag nodes from the left panel<br />
                  Connect output → input ports to chain them<br />
                  Press <kbd style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.2)', borderRadius: 3, padding: '1px 5px' }}>RUN</kbd> to execute
                </div>
              </div>
            )}

            {/* SVG edges */}
            <svg style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', overflow: 'visible', zIndex: 1, pointerEvents: 'none' }}>
              <g transform={`translate(${camera.x} ${camera.y}) scale(${camera.zoom})`}>
                {edgePaths.map(ep => (
                  <g key={ep.id}>
                    <path d={ep.path} stroke={ep.color} strokeWidth={1.5} fill="none"
                      strokeOpacity={ep.loop ? 0.85 : 0.5} strokeDasharray={ep.loop ? '6 4' : undefined} />
                    <path d={ep.path} stroke={ep.color} strokeWidth={12} fill="none" strokeOpacity={0}
                      style={{ pointerEvents: 'stroke', cursor: 'pointer' }}
                      onClick={() => { setEdges(prev => prev.filter(e => e.id !== ep.id)); setIsDirty(true) }}
                    />
                  </g>
                ))}
                {pendingPath && (
                  <path d={pendingPath} stroke="#00b4d8" strokeWidth={1.5} fill="none" strokeDasharray="5 4" strokeOpacity={0.6} />
                )}
              </g>
            </svg>

            {/* Nodes — the layer itself lets clicks through to the edges
                below; each node card takes pointer events back. */}
            <div style={{ position: 'absolute', inset: 0, zIndex: 2, pointerEvents: 'none', transformOrigin: '0 0', transform: `translate(${camera.x}px,${camera.y}px) scale(${camera.zoom})` }}>
              {nodes.map(node => (
                <CanvasNode
                  key={node.id}
                  node={node}
                  selected={selectedId === node.id}
                  zoom={camera.zoom}
                  onClick={() => { setSelectedId(node.id); setInspectorOpen(true) }}
                  onDelete={() => deleteNode(node.id)}
                  onConfigure={() => { setSelectedId(node.id); setInspectorOpen(true) }}
                  onHeaderMouseDown={(e) => {
                    dragRef.current = { type: 'node', nodeId: node.id, startX: e.clientX, startY: e.clientY, nx: node.x, ny: node.y }
                  }}
                  onOutputPortMouseDown={(e, portIdx) => startEdge(e, node.id, portIdx)}
                  onInputPortMouseUp={(portIdx) => completeEdge(node.id, portIdx)}
                />
              ))}
            </div>
          </div>
        )}

        {/* Inspector — only shown when user clicks Settings on a node */}
        {inspectorOpen && selectedNode && (
          <Inspector
            node={selectedNode}
            onConfigChange={updateConfig}
            onClose={() => setInspectorOpen(false)}
            onNavigate={onNavigate}
            liveSchemas={liveSchemas}
            automationIds={automationIds}
          />
        )}

      </div>

      {/* Ghost node following cursor during palette drag */}
      {ghost && (
        <div style={{
          position: 'fixed',
          left: ghost.x + 12, top: ghost.y - 16,
          pointerEvents: 'none',
          zIndex: 9999,
          background: 'linear-gradient(160deg,#0d1a28 0%,#091220 100%)',
          border: `1.5px solid ${ghost.template.color || catColor(ghost.template.category || '')}66`,
          borderRadius: 10,
          padding: '8px 14px',
          fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0',
          opacity: 0.85,
          boxShadow: '0 8px 24px rgba(0,0,0,.6)',
          display: 'flex', alignItems: 'center', gap: 7,
          userSelect: 'none',
          whiteSpace: 'nowrap',
        }}>
          <div style={{ width: 7, height: 7, borderRadius: '50%', background: ghost.template.color || catColor(ghost.template.category || ''), flexShrink: 0 }} />
          {ghost.template.label}
        </div>
      )}

      <style>{`
        @keyframes spin { to { transform: rotate(360deg); } }
        @keyframes nodePulse { 0%,100% { opacity:.4; } 50% { opacity:1; } }
      `}</style>
    </div>
  )
}

// ── Config field definitions per node type ────────────────────────────────────
// field: { key, label, type: 'text'|'textarea'|'select'|'number'|'password', options?, default? }
// Legacy (unprefixed) → new prefixed node type names.
// Mirrors legacyNodeTypes map in app.go.
const LEGACY_NODE_TYPES = {
  'google_sheets': 'service.google_sheets', 'gmail': 'service.gmail', 'google_drive': 'service.google_drive',
  'github': 'service.github', 'notion': 'service.notion', 'airtable': 'service.airtable',
  'jira': 'service.jira', 'linear': 'service.linear', 'asana': 'service.asana',
  'stripe': 'service.stripe', 'shopify': 'service.shopify', 'salesforce': 'service.salesforce',
  'hubspot': 'service.hubspot',
  'slack': 'comm.slack', 'discord': 'comm.discord', 'telegram': 'comm.telegram',
  'twilio': 'comm.twilio', 'whatsapp': 'comm.whatsapp',
  'email_send': 'comm.email_send', 'email_read': 'comm.email_read',
  'mysql': 'db.mysql', 'postgres': 'db.postgres', 'mongodb': 'db.mongodb', 'redis': 'db.redis',
  'datetime': 'data.datetime', 'crypto': 'data.crypto', 'html': 'data.html',
  'xml': 'data.xml', 'markdown': 'data.markdown', 'spreadsheet': 'data.spreadsheet',
  'compression': 'data.compression', 'write_binary_file': 'data.write_binary_file',
  'if': 'core.if', 'switch': 'core.switch', 'merge': 'core.merge', 'set': 'core.set',
  'code': 'core.code', 'filter': 'core.filter', 'sort': 'core.sort', 'limit': 'core.limit',
  'aggregate': 'core.aggregate', 'wait': 'core.wait',
  'http_request': 'http.request', 'http_response': 'http.response',
  'execute_command': 'system.execute_command', 'rss_read': 'system.rss_read',
  'read_write_file': 'system.read_write_file',
}
function normalizeNodeType(t) { return LEGACY_NODE_TYPES[t] || t }


function getConfigFields(nodeType) {
  const nt = normalizeNodeType(nodeType)
  if (NODE_CONFIG_FIELDS[nt]) return NODE_CONFIG_FIELDS[nt]
  // Generic fallback for browser/social nodes
  if (nt.startsWith('instagram.') || nt.startsWith('linkedin.') ||
      nt.startsWith('x.') || nt.startsWith('tiktok.')) {
    return BROWSER_NODE_GENERIC
  }
  return []
}

// ── Derive input/output ports from node type string ───────────────────────────
// deriveInputs / deriveOutputs(type, config) live in nodeConfigFields.js so
// they can be unit-tested; core.switch and ai.choose ports follow their cases.

const tbBtn = {
  background: 'transparent',
  border: '1px solid rgba(0,180,216,0.15)',
  borderRadius: 6, padding: '4px 8px',
  color: 'var(--text-muted)', cursor: 'pointer',
  display: 'flex', alignItems: 'center',
  transition: 'all 100ms',
}
