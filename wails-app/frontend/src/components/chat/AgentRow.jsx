import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Users, ChevronRight, ChevronDown, Loader, Check, X, Lock, HelpCircle } from 'lucide-react'
import { ChatMarkdown } from './ChatMarkdown.jsx'
import { UnconfinedWorkerWarning } from './ChatWarnings.jsx'
import { api } from '../../services/api.js'

// AgentRow is one dynamic-org worker (#226) in the lead's timeline: a
// compact line with its role, model and status, expandable to its brief and
// report. The live org stage (#228) shows the same workers graphically.

const mono = 'var(--font-mono)'
const STATUSES = ['queued', 'starting', 'working', 'waiting_lease', 'waiting_user', 'done', 'failed', 'cancelled', 'idle']

function StatusIcon({ status }) {
  if (status === 'done') return <Check size={10} color="var(--green-neon)" />
  if (status === 'failed') return <X size={10} color="var(--red)" />
  if (status === 'cancelled') return <X size={10} color="var(--text-muted)" />
  if (status === 'waiting_lease') return <Lock size={10} color="var(--yellow)" />
  if (status === 'waiting_user') return <HelpCircle size={10} color="var(--yellow)" />
  return <Loader size={10} className="chat-spin" color="var(--cyan)" />
}

// QuestionBox answers a worker's open question (#256) through
// `chat turn answer`. scope is the live turn's { conversationId, turnId }.
function QuestionBox({ agent, scope }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [state, setState] = useState({ sending: false, error: '' })
  const q = agent.question
  const canAnswer = !!scope?.conversationId && !!scope?.turnId
  const send = async (e) => {
    e.preventDefault()
    const answer = text.trim()
    if (!answer || state.sending) return
    setState({ sending: true, error: '' })
    try {
      await api.answerAgentQuestion(scope.conversationId, scope.turnId, agent.agentId, q.id, answer)
      setText('')
      setState({ sending: false, error: '' })
    } catch (err) {
      setState({ sending: false, error: String(err?.message || err) })
    }
  }
  return (
    <form onSubmit={send} data-testid="agent-question" style={{ margin: '0 8px 6px 30px', padding: '6px 8px', borderRadius: 6, border: '1px solid rgba(251,191,36,0.35)', background: 'rgba(251,191,36,0.06)', display: 'flex', flexDirection: 'column', gap: 5 }}>
      <div style={{ fontFamily: mono, fontSize: 10, color: '#fbbf24' }}>{t('agentRow.questionFor')} {q.text}</div>
      {canAnswer && (
        <div style={{ display: 'flex', gap: 6 }}>
          <input value={text} onChange={e => setText(e.target.value)} aria-label={t('agentRow.answerLabel')} placeholder={t('agentRow.answerPlaceholder')}
            style={{ flex: 1, background: '#020509', border: '1px solid rgba(0,180,216,0.2)', borderRadius: 5, padding: '4px 8px', color: '#e2e8f0', fontFamily: mono, fontSize: 10 }} />
          <button type="submit" className="btn btn-sm" disabled={!text.trim() || state.sending} style={{ fontSize: 10 }}>{t('agentRow.answer')}</button>
        </div>
      )}
      {state.error && <div role="alert" style={{ fontFamily: mono, fontSize: 9.5, color: 'var(--red)' }}>{state.error}</div>}
    </form>
  )
}

export function AgentRow({ agent, scope }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  if (!agent) return null
  const model = [agent.runtime, agent.model].filter(Boolean).join(' · ')
  const status = STATUSES.includes(agent.status) ? t(`agentRow.status.${agent.status}`) : agent.status
  return (
    <div data-testid="agent-row" data-agent={agent.agentId} data-status={agent.status}
      style={{ margin: '4px 0', border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6, background: 'rgba(0,180,216,0.03)' }}>
      <button type="button" onClick={() => setOpen(o => !o)} aria-expanded={open}
        style={{ display: 'flex', alignItems: 'center', gap: 6, width: '100%', padding: '5px 8px', background: 'transparent', border: 'none', cursor: 'pointer', color: 'inherit', textAlign: 'left' }}>
        {open ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        <Users size={11} color="var(--cyan)" />
        <span style={{ fontFamily: mono, fontSize: 10.5, color: '#e2e8f0', fontWeight: 600 }}>{agent.role || agent.agentId}</span>
        {model && <span style={{ fontFamily: mono, fontSize: 9, color: 'var(--cyan)' }}>{model}</span>}
        {agent.access && <span style={{ fontFamily: mono, fontSize: 8.5, color: 'var(--text-muted)' }}>{agent.access}</span>}
        <span style={{ flex: 1 }} />
        {agent.lastTool && agent.status === 'working' && <span style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)' }}>{agent.lastTool}</span>}
        <StatusIcon status={agent.status} />
        <span style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)' }}>{status}</span>
      </button>
      {agent.confinement === 'write-lease' && <UnconfinedWorkerWarning />}
      {agent.question && agent.status === 'waiting_user' && <QuestionBox agent={agent} scope={scope} />}
      {!open && agent.summary && (
        <div style={{ padding: '0 8px 5px 30px', fontFamily: mono, fontSize: 9.5, color: 'var(--text-secondary)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{agent.summary}</div>
      )}
      {open && (
        <div data-testid="agent-row-details" style={{ padding: '2px 10px 8px 30px', fontFamily: mono, fontSize: 9.5, color: 'var(--text-secondary)', display: 'flex', flexDirection: 'column', gap: 4 }}>
          {agent.brief && <div><b>{t('agentRow.brief')}</b> {agent.brief}</div>}
          {agent.why && <div style={{ color: 'var(--text-muted)' }}>{t('agentRow.staffing', { why: agent.why })}</div>}
          {agent.reassigned && <div style={{ color: 'var(--yellow)' }}>{t('agentRow.switchedFrom', { from: agent.reassigned })}</div>}
          {agent.skills?.length > 0 && <div>{t('agentRow.skills', { skills: agent.skills.join(', ') })}</div>}
          {agent.filesChanged?.length > 0 && <div>{t('agentRow.files', { files: agent.filesChanged.join(', ') })}</div>}
          {agent.tools > 0 && (
            <div>
              {t('agentRow.toolCalls', { count: agent.tools })}
              {agent.costUsd != null && <span title={agent.costEstimated ? t('agentRow.costEstimated') : undefined}>{` · ${agent.costEstimated ? '≈' : ''}$${agent.costUsd.toFixed(4)}`}</span>}
            </div>
          )}
          {agent.report && <ChatMarkdown content={agent.report} />}
        </div>
      )}
    </div>
  )
}
