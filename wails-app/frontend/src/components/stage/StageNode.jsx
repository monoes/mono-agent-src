import { memo, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Code2, Bot, User, Check, X, Lock, HelpCircle, Wrench, FileText, Coins } from 'lucide-react'
import { suggestIcon, iconUrl, CAT_COLOR } from '../orgdesigner/roleIcons.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import { LEAD_ID, isIdleVeteran } from '../../lib/orgStage.js'
import { isFresh } from './useStageMotion.js'
import { FullAccessBadge } from '../orgdesigner/fullAccess.jsx'

export const CARD_W = 180
export const CARD_H = 92

const PALETTE = Object.values(CAT_COLOR)
const RUNTIME_MARK = {
  claude: ['C', '#d97757'], codex: ['X', '#10a37f'], gemini: ['G', '#4285f4'], cursor: ['Cu', '#475569'],
  opencode: ['O', '#7c3aed'], cline: ['Cl', '#0a66c2'], dsh: ['D', '#4d6bfe'], qwen: ['Q', '#615ced'], copilot: ['Co', '#24292e'],
}
// The lead's team tools (#226), each worded on its own.
export const ORG_TOOLS = ['org_roster', 'org_spawn', 'org_wait', 'org_message', 'org_stop']
const EFFORT_LEVEL = { minimal: 1, low: 1, medium: 2, high: 3, xhigh: 4, max: 4 }

function hash(s) {
  let h = 0
  for (const c of String(s || '')) h = (h * 31 + c.charCodeAt(0)) | 0
  return Math.abs(h)
}

// roleColor is a node's accent: stable per role, from the Org Designer's
// category colors.
export function roleColor(node) {
  if (node.id === LEAD_ID) return '#00b4d8'
  return PALETTE[hash(node.agentType || node.role || node.id) % PALETTE.length]
}

// RuntimeMark stands in for a runtime's logo: its initial on its color.
export function RuntimeMark({ runtime }) {
  if (!runtime) return null
  const [letter, color] = RUNTIME_MARK[runtime] || [runtime.slice(0, 1).toUpperCase(), PALETTE[hash(runtime) % PALETTE.length]]
  return <span className="stage-rt" style={{ background: color }} title={runtimeLabel(runtime)} aria-hidden="true">{letter}</span>
}

function EffortPip({ effort }) {
  const level = EFFORT_LEVEL[effort]
  if (!level) return null
  return (
    <span className="stage-pip" title={effort} aria-hidden="true">
      {[1, 2, 3, 4].map(i => <i key={i} className={i <= level ? 'on' : ''} />)}
    </span>
  )
}

// ModelChip is a node's runtime and model, with effort as a pip. After a
// reassignment the old model shows crossed out and the new one slides in.
export function ModelChip({ node, fallback, now }) {
  const runtime = node.runtime || fallback?.runtime || ''
  const model = node.model || fallback?.model || ''
  const effort = node.effort || fallback?.effort || ''
  if (!runtime && !model) return null
  const prev = node.prevModel
  return (
    <span style={{ display: 'flex', gap: 3, minWidth: 0, overflow: 'hidden' }}>
      {prev && (
        <span className="stage-chip old" data-testid="stage-old-model">{prev.model || runtimeLabel(prev.runtime)}</span>
      )}
      <span className={`stage-chip${prev && isFresh(node.reassignedAt, now) ? ' fresh' : ''}`} data-testid="stage-model">
        <RuntimeMark runtime={runtime} />
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{model || runtimeLabel(runtime)}</span>
        <EffortPip effort={effort} />
      </span>
    </span>
  )
}

// useRoleIcon resolves a node's avatar from the Org Designer's icon
// manifest; null until it loads (or when it can't).
function useRoleIcon(node) {
  const [icon, setIcon] = useState(null)
  const key = node.id === LEAD_ID ? 'lead' : node.native ? '' : (node.agentType || node.role)
  useEffect(() => {
    if (!key) return undefined
    let live = true
    Promise.resolve()
      .then(() => suggestIcon({ id: `stage:${key}`, type: key === 'lead' ? 'boss' : node.agentType, title: node.role }))
      .then(id => { if (live && id) setIcon(id) })
      .catch(() => {})
    return () => { live = false }
    // The role is fixed once a node exists.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  return icon
}

export function formatCost(cost, estimated) {
  if (cost == null) return ''
  const s = cost < 0.01 ? cost.toFixed(4) : cost.toFixed(2)
  return `${estimated ? '≈' : ''}$${s}`
}

export function formatTokens(n) {
  if (n == null) return ''
  return n >= 1000 ? `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k` : String(n)
}

// doingText words what a node is doing, per tool kind.
export function useDoingText() {
  const { t } = useTranslation()
  return (node) => {
    const d = node.doing
    if (!d) return ''
    if (d.vault) return t('stage.doing.vault')
    if (d.kind === 'org' && ORG_TOOLS.includes(d.name)) return t(`stage.orgTool.${d.name}`)
    const key = ['edit', 'write', 'patch', 'read', 'shell', 'web', 'search', 'task', 'mcp', 'todo', 'org'].includes(d.kind) ? d.kind : 'other'
    return t(`stage.doing.${key}`, { target: d.target || d.name, name: d.name })
  }
}

function StatusGlyph({ status }) {
  if (status === 'done') return <Check size={11} color="var(--green-neon, #22c55e)" aria-hidden="true" />
  if (status === 'failed') return <X size={11} color="#ef4444" aria-hidden="true" />
  if (status === 'waiting_lease') return <Lock size={10} color="#fbbf24" aria-hidden="true" />
  if (status === 'waiting_user') return <HelpCircle size={10} color="#fbbf24" aria-hidden="true" />
  return null
}

// StageNode is one agent on the stage: role, model chip, status ring, what
// it is doing now, its meters and XP. It is a button: Enter or a click
// selects it.
export const StageNode = memo(function StageNode({ node, x, y, selected, leadInfo, now, onSelect }) {
  const { t } = useTranslation()
  const doingText = useDoingText()
  const icon = useRoleIcon(node)
  const color = roleColor(node)
  const isLead = node.id === LEAD_ID
  // A running org's lead is its boss, named after the role (#229).
  const title = isLead ? node.role || t('bubbles.lead') : node.role || (node.native ? t('stage.subagent') : node.id)
  const statusLabel = t(`stage.status.${node.status}`, { defaultValue: node.status })
  const working = node.status === 'working' || node.status === 'starting'
  let line = ''
  if (node.status === 'waiting_lease') line = t('stage.waitingLease', { lease: t(`stage.lease.${node.statusDetail === 'browser' ? 'browser' : 'pen'}`) })
  // A reported native subagent's progress summary (#230) says more than its
  // last finished call.
  else if (node.doing?.active || working) line = (node.doing?.active && doingText(node)) || (node.native && node.statusDetail) || doingText(node) || t('stage.thinking')
  else if (node.status === 'waiting_user') line = t('stage.status.waiting_user')
  else if (node.status === 'queued') line = t('stage.status.queued')
  else if (isIdleVeteran(node)) line = t('stage.veteranIdle')
  else line = node.summary || doingText(node)
  const unsandboxed = node.confinement === 'write-lease'
  const Fallback = isLead ? Code2 : node.native ? Bot : User
  const cost = formatCost(node.costUsd, node.costEstimated)
  const tokens = node.tokensIn != null || node.tokensOut != null ? `${formatTokens(node.tokensIn ?? 0)}/${formatTokens(node.tokensOut ?? 0)}` : ''
  const cls = [
    'stage-card',
    node.native && 'native',
    isIdleVeteran(node) && 'veteran',
    selected && 'selected',
    node.needsYou && 'needs',
    !isLead && isFresh(node.spawnAt, now) && 'fresh',
    isFresh(node.statusAt, now) && 'fresh-status',
  ].filter(Boolean).join(' ')
  return (
    <button type="button" className={cls} data-testid="stage-node" data-agent={node.id} data-status={node.status}
      title={isIdleVeteran(node) ? t('stage.veteranHelp') : undefined}
      aria-pressed={selected} onClick={() => onSelect(node.id)}
      aria-label={[t('stage.nodeLabel', { name: title, status: statusLabel, doing: line }), unsandboxed && t('ui8Sandbox.agentLabel')].filter(Boolean).join('. ')}
      style={{ left: x, top: y, borderTopColor: color }}>
      {node.needsYou && <span className="stage-badge needs" data-testid="stage-needs-you">{t('stage.needsYou')}</span>}
      {node.limited && <span className="stage-badge limited" title={t('stage.limitedHelp')}>{t('stage.limited')}</span>}
      {node.fullAccess && <span className="stage-badge access"><FullAccessBadge entry={node.fullAccess} compact /></span>}
      {unsandboxed && <span className="stage-badge unsandboxed" data-testid="stage-unsandboxed" title={t('ui8Sandbox.agentLabel')}>{t('ui8Sandbox.badge')}</span>}
      <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
        <span style={{ width: 20, height: 20, borderRadius: 6, flexShrink: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', background: `${color}22`, color }}>
          {icon ? <img src={iconUrl(icon)} alt="" width={16} height={16} /> : <Fallback size={12} />}
        </span>
        <span style={{ flex: 1, minWidth: 0, fontFamily: 'var(--font-display)', fontSize: 11.5, fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{title}</span>
        <StatusGlyph status={node.status} />
        <span className="stage-ring" data-status={node.status} title={statusLabel} />
      </span>
      <ModelChip node={node} fallback={isLead ? leadInfo : null} now={now} />
      <span className="stage-doing" data-testid="stage-doing">{line}</span>
      <span className="stage-meta">
        <span title={t('stage.toolsUsed')}><Wrench size={8} aria-hidden="true" />{node.tools}</span>
        <span title={t('stage.filesTouched')}><FileText size={8} aria-hidden="true" />{node.files.length}</span>
        {tokens && <span title={t('stage.tokens')}>{tokens}</span>}
        {cost && <span title={node.costEstimated ? t('stage.costEstimated') : t('stage.cost')}><Coins size={8} aria-hidden="true" />{cost}</span>}
      </span>
    </button>
  )
})
