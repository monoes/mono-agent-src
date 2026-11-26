import { useState } from 'react'
import { CheckCircle, XCircle, ChevronDown, ChevronRight, MessageCircleQuestion, ShieldAlert, KeyRound } from 'lucide-react'
import { ApprovalCommand, ApprovalInput } from '../../components/ApprovalCommand.jsx'
import { NoRequestID } from '../../components/NoRequestID.jsx'

// ── 4. Org Decision / Question / Gate Card ────────────────────
export function OrgDecisionCard({ item, onAnswerQuestion, onApproveAction, onDenyAction, onApproveGate, onRejectGate }) {
  const [expanded, setExpanded] = useState(true)
  const [answerText, setAnswerText] = useState('')
  const [loading, setLoading] = useState(null)

  const handleAnswer = async () => {
    if (!answerText.trim()) return
    setLoading('answer')
    try {
      await onAnswerQuestion(item.orgName, item.id, answerText.trim())
    } finally {
      setLoading(null)
    }
  }

  const handleApprove = async () => {
    setLoading('approve')
    try {
      if (item.kind === 'approval') {
        await onApproveAction(item)
      } else if (item.kind === 'gate') {
        await onApproveGate(item.orgName, item.id)
      }
    } finally {
      setLoading(null)
    }
  }

  const handleReject = async () => {
    setLoading('reject')
    try {
      if (item.kind === 'approval') {
        await onDenyAction(item)
      } else if (item.kind === 'gate') {
        await onRejectGate(item.orgName, item.id)
      }
    } finally {
      setLoading(null)
    }
  }

  // Only a request with an id can be resolved on its own.
  const noRequestID = item.kind === 'approval' && !item.request_id

  const KindIcon = item.kind === 'question' ? MessageCircleQuestion : item.kind === 'gate' ? ShieldAlert : KeyRound

  return (
    <div data-testid="org-decision" style={{
      background: 'linear-gradient(160deg,#181024 0%,#100a1c 100%)',
      border: '1.5px solid rgba(139,92,246,0.25)',
      borderRadius: 10,
      marginBottom: 16,
      overflow: 'hidden',
    }}>
      <div
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '12px 16px',
          background: 'rgba(139,92,246,0.08)',
          borderBottom: expanded ? '1px solid rgba(139,92,246,0.15)' : 'none',
          cursor: 'pointer',
        }}
        onClick={() => setExpanded(v => !v)}
      >
        <KindIcon size={15} color="#8b5cf6" style={{ flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span style={{
              padding: '1px 6px', borderRadius: 4,
              fontSize: 10, fontFamily: 'var(--font-mono)', fontWeight: 700,
              background: 'rgba(139,92,246,0.2)', color: '#a78bfa', border: '1px solid rgba(139,92,246,0.3)',
            }}>
              ORG: {item.orgName}
            </span>
            <span style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>
              {item.title || item.action || item.name || `${item.kind} for ${item.role}`}
            </span>
          </div>
          <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
            Role: {item.role || 'operator'} {item.since ? `· waiting ${item.since}` : ''}
          </div>
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(139,92,246,0.15)', color: '#a78bfa',
          fontSize: 11, fontFamily: 'var(--font-mono)', flexShrink: 0,
        }}>{item.kind}</span>
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      {expanded && (
        <div style={{ padding: 16 }}>
          <div style={{
            padding: '9px 12px', background: 'rgba(255,255,255,0.04)', borderRadius: 6,
            border: '1px solid rgba(255,255,255,0.07)', fontSize: 13, color: 'var(--text-secondary)',
            marginBottom: 16, lineHeight: 1.5,
          }}>
            {item.text || item.name}
          </div>

          {item.kind === 'approval' && (
            <div style={{ marginBottom: 16 }}>
              <ApprovalCommand command={item.command} truncated={item.command_truncated} cwd={item.cwd} />
              {!item.command && <ApprovalInput input={item.input} />}
            </div>
          )}

          {item.kind === 'question' ? (
            <div style={{ display: 'flex', gap: 8 }}>
              <input
                type="text"
                value={answerText}
                onChange={e => setAnswerText(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter' && answerText.trim()) handleAnswer() }}
                placeholder="Type your answer…"
                style={{
                  flex: 1, padding: '7px 10px',
                  background: 'rgba(139,92,246,0.05)', border: '1px solid rgba(139,92,246,0.25)',
                  borderRadius: 6, color: 'var(--text-primary)', fontSize: 12, outline: 'none',
                }}
              />
              <button
                onClick={handleAnswer}
                disabled={!answerText.trim() || !!loading}
                style={{
                  padding: '7px 16px', background: 'rgba(139,92,246,0.2)', border: '1px solid rgba(139,92,246,0.4)',
                  borderRadius: 6, color: '#a78bfa', fontWeight: 600, fontSize: 12, cursor: !answerText.trim() ? 'not-allowed' : 'pointer',
                }}
              >
                {loading === 'answer' ? 'Answering…' : 'Submit Answer'}
              </button>
            </div>
          ) : noRequestID ? (
            <NoRequestID org={item.orgName} role={item.role} action={item.action} />
          ) : (
            <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)' }}>
              <button
                onClick={handleReject}
                disabled={!!loading}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6,
                  padding: '7px 14px',
                  background: loading === 'reject' ? 'rgba(239,68,68,0.15)' : 'rgba(239,68,68,0.1)',
                  border: '1px solid rgba(239,68,68,0.3)',
                  borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                  color: '#ef4444', fontSize: 12, fontWeight: 500,
                  opacity: loading && loading !== 'reject' ? 0.5 : 1,
                }}
              >
                <XCircle size={13} />
                {loading === 'reject' ? 'Denying…' : (item.kind === 'gate' ? 'Reject Gate' : 'Deny')}
              </button>
              <button
                onClick={handleApprove}
                disabled={!!loading}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6,
                  padding: '7px 16px',
                  background: loading === 'approve' ? 'rgba(16,185,129,0.25)' : 'rgba(16,185,129,0.15)',
                  border: '1px solid rgba(16,185,129,0.4)',
                  borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                  color: '#10b981', fontSize: 12, fontWeight: 600,
                  opacity: loading && loading !== 'approve' ? 0.5 : 1,
                }}
              >
                <CheckCircle size={13} />
                {loading === 'approve' ? 'Approving…' : (item.kind === 'gate' ? 'Approve Gate' : 'Approve')}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
