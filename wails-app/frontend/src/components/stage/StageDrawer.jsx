import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { X, Square } from 'lucide-react'
import { ChatMarkdown } from '../chat/ChatMarkdown.jsx'
import { ChatTimeline } from '../chat/ChatTimeline.jsx'
import { LEAD_ID } from '../../lib/orgStage.js'
import { ModelChip, formatCost, formatTokens, roleColor } from './StageNode.jsx'
import { FullAccessBadge } from '../orgdesigner/fullAccess.jsx'

// agentTimeline is an agent's own text and tool calls as chatReducer-shaped
// state, so ChatTimeline (and NativeToolCard under it) renders them exactly
// as it renders the lead's. calls is the turn's (chatReducer's agentCalls);
// the stage keeps only their order. A native subagent's calls sit inside
// its caller's Task call, which isn't in its own list, so they show flat.
function agentTimeline(node, calls) {
  const own = {}
  for (const id of node.callOrder) {
    const call = calls?.[id]
    if (call) own[id] = node.native && call.parentCallId ? { ...call, parentCallId: undefined } : call
  }
  const parts = node.parts.filter(p => p.kind === 'text' || own[p.callId])
  return { parts, calls: own, notices: [] }
}

function pct(v) {
  return typeof v === 'number' ? `${Math.round(v * 100)}%` : ''
}

// StageDrawer is a clicked node's detail (#228): its brief, why it was
// staffed the way it was, model, effort and access, the messages it got and
// sent, and its tool cards (from calls, the turn's agentCalls). onStop
// stops just this agent; without it (a finished turn) the button is
// disabled and says so. canStop false leaves the button out (a running
// org's roles stop with the org).
export function StageDrawer({ node, calls, leadInfo, turnId, isLive, onClose, onStop, canStop = true, onNavigate }) {
  const { t } = useTranslation()
  const timeline = useMemo(() => agentTimeline(node, calls), [node, calls])
  const isLead = node.id === LEAD_ID
  const title = isLead ? node.role || t('bubbles.lead') : node.role || (node.native ? t('stage.subagent') : node.id)
  const running = ['queued', 'starting', 'working', 'waiting_lease', 'waiting_user'].includes(node.status)
  const cost = formatCost(node.costUsd, node.costEstimated)
  const confidences = [
    node.pickConfidence != null && t('stage.pickConfidence', { value: pct(node.pickConfidence) }),
    node.jevConfidence != null && t('stage.jevConfidence', { value: pct(node.jevConfidence) }),
  ].filter(Boolean)
  return (
    <aside className="stage-drawer" data-testid="stage-drawer" data-agent={node.id} aria-label={t('stage.drawerLabel', { name: title })}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 12px', borderBottom: '1px solid var(--border)', borderTop: `2px solid ${roleColor(node)}` }}>
        <span style={{ fontFamily: 'var(--font-display)', fontSize: 13, fontWeight: 600, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{title}</span>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)' }}>{t(`stage.status.${node.status}`, { defaultValue: node.status })}</span>
        <span style={{ flex: 1 }} />
        {node.fullAccess && <FullAccessBadge entry={node.fullAccess} />}
        {canStop && !isLead && !node.native && (
          <button type="button" className="btn btn-ghost btn-sm" data-testid="stage-stop"
            disabled={!onStop || !running} onClick={() => onStop?.(node.id)}
            title={onStop ? t('stage.stopAgent') : t('stage.stopUnavailable')} style={{ gap: 4, fontSize: 10 }}>
            <Square size={10} /> {t('stage.stop')}
          </button>
        )}
        <button type="button" className="btn btn-ghost btn-sm" onClick={onClose} aria-label={t('stage.closeDrawer')} title={t('stage.closeDrawer')} style={{ padding: '3px 6px' }}>
          <X size={13} />
        </button>
      </div>
      <div className="stage-drawer-body">
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5, alignItems: 'center' }}>
          <ModelChip node={node} fallback={isLead ? leadInfo : null} now={0} />
          {node.access && <span className="stage-chip" style={{ color: 'var(--text-secondary)' }}>{t('stage.access', { access: node.access })}</span>}
          {node.effort && <span className="stage-chip" style={{ color: 'var(--text-secondary)' }}>{t('stage.effort', { effort: node.effort })}</span>}
          {node.branch && <span className="stage-chip" data-testid="stage-branch" title={t('stage.branchHelp')} style={{ color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>{t('stage.branch', { branch: node.branch })}</span>}
        </div>
        {node.prevModel && (
          <div style={{ color: '#fbbf24' }}>{t('stage.reassignedFrom', { model: `${node.prevModel.runtime}/${node.prevModel.model || t('ui6OrgsStage.default_model')}`, reason: node.prevModel.reason })}</div>
        )}
        {node.brief && (
          <section><h4>{t('stage.brief')}</h4><ChatMarkdown content={node.brief} /></section>
        )}
        {(node.why || confidences.length > 0) && (
          <section data-testid="stage-why">
            <h4>{t('stage.why')}</h4>
            {node.why && <div>{node.why}</div>}
            {confidences.length > 0 && <div style={{ color: 'var(--text-muted)', marginTop: 2 }}>{confidences.join(' · ')}</div>}
          </section>
        )}
        {node.skills.length > 0 && <section><h4>{t('stage.skills')}</h4><div>{node.skills.join(', ')}</div></section>}
        <section>
          <h4>{t('stage.meters')}</h4>
          <div>
            {[
              t('stage.toolsCount', { count: node.tools }),
              t('stage.filesCount', { count: node.files.length }),
              node.tokensIn != null && t('stage.tokensInOut', { input: formatTokens(node.tokensIn), output: formatTokens(node.tokensOut ?? 0) }),
              cost,
              node.durationMs > 0 && t('stage.seconds', { value: Math.round(node.durationMs / 1000) }),
            ].filter(Boolean).join(' · ')}
          </div>
          {node.limited && <div style={{ color: 'var(--text-muted)', marginTop: 2 }}>{t('stage.limitedHelp')}</div>}
          {node.files.length > 0 && <div style={{ marginTop: 3, wordBreak: 'break-all' }}>{node.files.join(', ')}</div>}
        </section>
        {node.messages.length > 0 && (
          <section data-testid="stage-transcript">
            <h4>{t('stage.transcript')}</h4>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
              {node.messages.map(m => (
                <div key={`${m.seq}-${m.direction}`} className="stage-msg" data-direction={m.direction}>
                  <div style={{ fontSize: 8.5, color: 'var(--text-muted)', marginBottom: 2 }}>{t(`stage.direction.${m.direction}`, { defaultValue: m.direction, from: m.from, to: m.to })}</div>
                  <ChatMarkdown content={m.text} />
                </div>
              ))}
            </div>
          </section>
        )}
        {timeline.parts.length > 0 && (
          <section data-testid="stage-tools">
            <h4>{t('stage.work')}</h4>
            <ChatTimeline state={timeline} turnId={`${turnId}-${node.id}`} isLive={isLive} onNavigate={onNavigate} />
          </section>
        )}
        {isLead && <div style={{ color: 'var(--text-muted)' }}>{t('stage.leadInChat')}</div>}
      </div>
    </aside>
  )
}
