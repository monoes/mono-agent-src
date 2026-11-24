import { useState } from 'react'
import { CheckCircle, XCircle, ChevronDown, ChevronRight, Send, ExternalLink, Users } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { formatDate, PLATFORM_PROFILE_URL } from './hilShared.jsx'
import { SuggestionChip } from './HILCards.jsx'

// ── 3. Lead / Candidate Outreach Approval Card ────────────────
export function LeadApprovalCard({ item, onApprove, onReject, onOpenProfile }) {
  const [expanded, setExpanded] = useState(true)
  const [editedIntro, setEditedIntro] = useState(item.introduction || '')
  const [loading, setLoading] = useState(null) // 'approve' | 'approve_send' | 'reject'

  const handleApproveOnly = async () => {
    setLoading('approve')
    try {
      await onApprove(item.id, editedIntro, false)
    } finally {
      setLoading(null)
    }
  }

  const handleApproveAndSend = async () => {
    setLoading('approve_send')
    try {
      await onApprove(item.id, editedIntro, true)
    } finally {
      setLoading(null)
    }
  }

  const handleReject = async () => {
    setLoading('reject')
    try {
      await onReject(item.id)
    } finally {
      setLoading(null)
    }
  }

  const platformColor = item.platform?.toUpperCase() === 'LINKEDIN' ? '#0077b5' : '#00b4d8'
  const rawUrl = item.profile_url || PLATFORM_PROFILE_URL[item.platform?.toUpperCase()]?.(item.platform_username)
  const profileUrl = rawUrl
    ? (/^https?:\/\//i.test(rawUrl) ? rawUrl : `https://${rawUrl}`)
    : null

  return (
    <div style={{
      background: 'linear-gradient(160deg,#0a1926 0%,#06101c 100%)',
      border: '1.5px solid rgba(0,180,216,0.25)',
      borderRadius: 10,
      marginBottom: 16,
      overflow: 'hidden',
    }}>
      {/* Header */}
      <div
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '12px 16px',
          background: 'rgba(0,180,216,0.08)',
          borderBottom: expanded ? '1px solid rgba(0,180,216,0.14)' : 'none',
          cursor: 'pointer',
        }}
        onClick={() => setExpanded(v => !v)}
      >
        <Users size={15} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>
              {item.full_name || item.platform_username}
            </span>
            <span style={{
              padding: '1px 6px', borderRadius: 4,
              fontSize: 10, fontFamily: 'var(--font-mono)', fontWeight: 600,
              background: `${platformColor}25`, color: platformColor, border: `1px solid ${platformColor}40`,
            }}>
              {item.platform?.toUpperCase() || 'LEAD'}
            </span>
            {item.platform_username && (
              profileUrl ? (
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation()
                    api.openURL(profileUrl)
                  }}
                  title={`Open ${profileUrl}`}
                  style={{
                    background: 'none',
                    border: 'none',
                    padding: 0,
                    fontSize: 11,
                    color: '#00b4d8',
                    fontFamily: 'var(--font-mono)',
                    cursor: 'pointer',
                    textDecoration: 'underline',
                  }}
                >
                  @{item.platform_username}
                </button>
              ) : (
                <span style={{ fontSize: 11, color: '#00b4d8', fontFamily: 'var(--font-mono)' }}>
                  @{item.platform_username}
                </span>
              )
            )}
          </div>
          {item.job_title && (
            <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {item.job_title}
            </div>
          )}
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(0,180,216,0.15)', color: '#00b4d8',
          fontSize: 11, fontFamily: 'var(--font-mono)', flexShrink: 0,
        }}>lead pitch</span>
        {item.suggestion?.suggest && (
          <SuggestionChip
            choice={item.suggestion.suggest}
            p={item.suggestion.p}
            detail={item.suggestion.intro_fit ? `intro ${item.suggestion.intro_fit.replace(/_/g, ' ')}` : ''}
            model={item.suggestion.model}
          />
        )}
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      {expanded && (
        <div style={{ padding: 16 }}>
          <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 12, fontSize: 11, color: 'var(--text-muted)' }}>
            {profileUrl && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  api.openURL(profileUrl)
                }}
                title={`Open on ${item.platform || 'web'}`}
                style={{
                  background: 'none',
                  border: 'none',
                  padding: 0,
                  color: '#00b4d8',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 4,
                  cursor: 'pointer',
                  fontSize: 11,
                  textDecoration: 'underline',
                }}
              >
                View Profile <ExternalLink size={11} />
              </button>
            )}
            {item.created_at && (
              <span style={{ fontFamily: 'var(--font-mono)' }}>
                Captured: {formatDate(item.created_at)}
              </span>
            )}
            {onOpenProfile && (
              <button
                onClick={(e) => { e.stopPropagation(); onOpenProfile(item.id) }}
                style={{
                  background: 'none', border: 'none', padding: 0,
                  color: 'var(--text-secondary)', cursor: 'pointer',
                  fontSize: 11, textDecoration: 'underline',
                }}
              >
                Open in CRM
              </button>
            )}
          </div>

          <div style={{ marginBottom: 16 }}>
            <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: '#00b4d8', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 4 }}>
              Generated Outreach Pitch <span style={{ color: 'var(--text-muted)', fontWeight: 400 }}>(editable)</span>
            </div>
            <textarea
              value={editedIntro}
              onChange={e => setEditedIntro(e.target.value)}
              rows={4}
              style={{
                width: '100%',
                boxSizing: 'border-box',
                padding: '9px 12px',
                background: 'rgba(0,180,216,0.04)',
                border: '1px solid rgba(0,180,216,0.25)',
                borderRadius: 6,
                fontSize: 13,
                color: 'var(--text-primary)',
                fontFamily: 'inherit',
                resize: 'vertical',
                outline: 'none',
                lineHeight: 1.6,
              }}
              onFocus={e => { e.target.style.borderColor = 'rgba(0,180,216,0.6)' }}
              onBlur={e => { e.target.style.borderColor = 'rgba(0,180,216,0.25)' }}
            />
          </div>

          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)', flexWrap: 'wrap' }}>
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
              {loading === 'reject' ? 'Rejecting…' : 'Reject'}
            </button>
            <button
              onClick={handleApproveOnly}
              disabled={!!loading}
              title="Save changes and mark approved without dispatching"
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '7px 14px',
                background: 'rgba(255,255,255,0.06)',
                border: '1px solid rgba(255,255,255,0.15)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: 'var(--text-secondary)', fontSize: 12, fontWeight: 500,
                opacity: loading && loading !== 'approve' ? 0.5 : 1,
              }}
            >
              <CheckCircle size={13} />
              {loading === 'approve' ? 'Saving…' : 'Approve Only'}
            </button>
            <button
              onClick={handleApproveAndSend}
              disabled={!!loading}
              title="Save changes, approve, and trigger outreach dispatch workflow"
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '7px 16px',
                background: loading === 'approve_send' ? 'rgba(16,185,129,0.25)' : 'rgba(16,185,129,0.15)',
                border: '1px solid rgba(16,185,129,0.4)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#10b981', fontSize: 12, fontWeight: 600,
                opacity: loading && loading !== 'approve_send' ? 0.5 : 1,
              }}
            >
              <Send size={13} />
              {loading === 'approve_send' ? 'Dispatching…' : 'Approve & Send'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
