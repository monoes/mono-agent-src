import { useState, useEffect, useCallback, useRef } from 'react'
import {
  CheckCircle, XCircle, Clock, RefreshCw, ChevronDown, ChevronRight,
  AlertTriangle, Mail, UserCheck, X, Send, ExternalLink, Users,
  MessageCircleQuestion, ShieldAlert, KeyRound,
} from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { api, notify } from '../services/api.js'
import { usePageVisibleRef, useVisibleCatchUp } from '../lib/usePageVisible.js'
import { CommsLinks } from '../components/CommsLinks.jsx'
import { emitCommsChanged } from '../lib/commsStatus.js'
import { BrowserSubmitDetails, isBrowserSubmit } from '../components/hil/BrowserSubmitDetails.jsx'
import { BrowserSiteDetails, isBrowserSite } from '../components/hil/BrowserSiteDetails.jsx'
import { PersonItemsInbox } from '../components/bubbles/PersonItemViews.jsx'
import { usePersonItemsContext } from '../components/bubbles/PersonItemsContext.jsx'

const GetHILItems          = WailsApp.GetHILItems          ?? (async () => [])
const ApproveHIL           = WailsApp.ApproveHIL           ?? (async () => {})
const RejectHIL            = WailsApp.RejectHIL            ?? (async () => {})
const GetDraftPersonMessages   = WailsApp.GetDraftPersonMessages   ?? (async () => [])
const SendDraftPersonMessage   = WailsApp.SendDraftPersonMessage   ?? (async () => {})
const RejectDraftPersonMessage = WailsApp.RejectDraftPersonMessage ?? (async () => {})

function formatDate(s) {
  if (!s) return ''
  const d = new Date(s.replace(' ', 'T'))
  return isNaN(d) ? s : d.toLocaleString()
}

function isImageValue(value) {
  if (typeof value !== 'string') return false
  return /\.(png|jpe?g|gif|webp|svg|bmp)(\?.*)?$/i.test(value) ||
    value.startsWith('data:image/') ||
    value.startsWith('blob:')
}

const PLATFORM_PROFILE_URL = {
  INSTAGRAM: (u) => `https://www.instagram.com/${u}/`,
  LINKEDIN:  (u) => `https://www.linkedin.com/in/${u}/`,
  X:         (u) => `https://x.com/${u}`,
  TIKTOK:    (u) => `https://www.tiktok.com/@${u}`,
}

function ReadonlyField({ label, value }) {
  const display = typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value ?? '')
  const isImg = isImageValue(display)
  const isUrl = typeof display === 'string' && /^https?:\/\//i.test(display)
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 3 }}>
        {label}
      </div>
      {isImg ? (
        <div style={{ borderRadius: 6, overflow: 'hidden', border: '1px solid rgba(255,255,255,0.07)', background: 'rgba(255,255,255,0.04)' }}>
          <img src={display} alt={label} style={{ maxWidth: '100%', maxHeight: 200, display: 'block', objectFit: 'contain' }} />
          <div style={{ padding: '4px 8px', fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', wordBreak: 'break-all' }}>{display}</div>
        </div>
      ) : (
        <div style={{
          padding: '7px 10px',
          background: 'rgba(255,255,255,0.04)',
          borderRadius: 6,
          border: '1px solid rgba(255,255,255,0.07)',
          fontSize: 13,
          color: 'var(--text-secondary)',
          wordBreak: 'break-all',
        }}>
          {isUrl ? (
            <button
              type="button"
              onClick={() => api.openURL(display)}
              style={{
                background: 'none', border: 'none', padding: 0,
                color: '#00b4d8', cursor: 'pointer', textAlign: 'left',
                wordBreak: 'break-all', fontSize: 13, textDecoration: 'underline',
                display: 'inline-flex', alignItems: 'center', gap: 4,
              }}
            >
              {display} <ExternalLink size={11} style={{ flexShrink: 0 }} />
            </button>
          ) : display}
        </div>
      )}
    </div>
  )
}

function EditableField({ label, value, onChange }) {
  const isLong = typeof value === 'string' && value.length > 80
  return (
    <div style={{ marginBottom: 12 }}>
      <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: '#00b4d8', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 4 }}>
        {label} <span style={{ color: 'var(--text-muted)', fontWeight: 400 }}>(editable)</span>
      </div>
      <textarea
        value={typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value ?? '')}
        onChange={e => onChange(e.target.value)}
        rows={isLong ? 6 : 3}
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
  )
}

// ── 1. Workflow Pause Card ────────────────────────────────────
// TypeSafe Jev suggestions (plan WS5). The CLI stores them: workflow items
// carry theirs in node_config.suggestion ({choice, p, risk}), people staged
// for review in suggestion ({suggest, p, intro_fit}). Suggestions only
// inform the reviewer; nothing here approves or rejects on its own.
const SUGGESTION_COLORS = {
  approve: '#22c55e',
  reject: '#ef4444',
  needs_human: '#f59e0b',
}

export function suggestionOf(item) {
  const s = item?.node_config?.suggestion
  return s && typeof s.choice === 'string' ? s : null
}

// Most confident suggestions first; items without one keep their order after.
export function sortBySuggestion(items) {
  return items
    .map((item, i) => ({ item, i, p: suggestionOf(item)?.p }))
    .sort((a, b) => {
      const pa = typeof a.p === 'number' ? a.p : -1
      const pb = typeof b.p === 'number' ? b.p : -1
      return pb - pa || a.i - b.i
    })
    .map(x => x.item)
}

export function SuggestionChip({ choice, p, detail }) {
  if (!choice) return null
  const color = SUGGESTION_COLORS[choice] || '#94a3b8'
  const pct = typeof p === 'number' ? `${Math.round(p * 100)}%` : ''
  const label = choice.replace(/_/g, ' ')
  return (
    <span
      data-testid="jev-suggestion"
      title={`TypeSafe Jev suggests: ${label}${pct ? ` (${pct} confident)` : ''}${detail ? ` · ${detail}` : ''}. A suggestion only; you decide.`}
      style={{
        padding: '2px 8px', borderRadius: 10, flexShrink: 0,
        background: `${color}1f`, color, border: `1px solid ${color}55`,
        fontSize: 11, fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap',
      }}
    >
      Suggested: {label}{pct && ` ${pct}`}{detail && ` · ${detail}`}
    </span>
  )
}

function HILCard({ item, onApprove, onReject, onProfile, onNavigate }) {
  const [expanded, setExpanded] = useState(true)
  const [editedValues, setEditedValues] = useState({ ...item.editable_data })
  const [loading, setLoading] = useState(null) // 'approve' | 'reject'

  const editableKeys = item.node_config?.editable_fields ?? Object.keys(item.editable_data ?? {})
  const readonlyKeys = item.node_config?.readonly_fields ?? Object.keys(item.readonly_data ?? {})

  const handleApprove = async () => {
    setLoading('approve')
    try {
      const typed = Object.fromEntries(
        Object.entries(editedValues).map(([k, v]) => {
          if (typeof v !== 'string') return [k, v]
          try { return [k, JSON.parse(v)] } catch { return [k, v] }
        })
      )
      await onApprove(item.id, JSON.stringify(typed))
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

  return (
    <div style={{
      background: 'linear-gradient(160deg,#0d1a28 0%,#091220 100%)',
      border: '1.5px solid rgba(0,180,216,0.18)',
      borderRadius: 10,
      marginBottom: 16,
      overflow: 'hidden',
    }}>
      {/* Header */}
      <div
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '12px 16px',
          background: 'rgba(0,180,216,0.07)',
          borderBottom: expanded ? '1px solid rgba(0,180,216,0.12)' : 'none',
          cursor: 'pointer',
        }}
        onClick={() => setExpanded(v => !v)}
      >
        <Clock size={14} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>
            {item.node_name}
            {item.workflow_name && (
              <span style={{ fontWeight: 400, color: 'var(--text-muted)', marginLeft: 6 }}>
                — {item.workflow_name}
              </span>
            )}
          </div>
          <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
            {formatDate(item.created_at)} · exec: {item.execution_id?.slice(0, 8) ?? '—'}…
          </div>
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(0,180,216,0.12)', color: '#00b4d8',
          fontSize: 11, fontFamily: 'var(--font-mono)', flexShrink: 0,
        }}>{isBrowserSubmit(item) || isBrowserSite(item) ? 'browser' : 'workflow'}</span>
        {suggestionOf(item) && (
          <SuggestionChip
            choice={suggestionOf(item).choice}
            p={suggestionOf(item).p}
            detail={suggestionOf(item).risk ? `risk ${suggestionOf(item).risk}` : ''}
          />
        )}
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      {(item.person_id || item.hil_ref) && (
        <div style={{ padding: '6px 16px' }}>
          <CommsLinks personId={item.person_id} hilRef={item.hil_ref || ''} name={item.person_full_name || item.person_platform_username} onProfile={onProfile} onNavigate={onNavigate} />
        </div>
      )}
      {expanded && (
        <div style={{ padding: 16 }}>
          {isBrowserSubmit(item) ? <BrowserSubmitDetails item={item} /> : isBrowserSite(item) ? <BrowserSiteDetails item={item} /> : readonlyKeys.length > 0 && (
            <div style={{ marginBottom: 20 }}>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 10, paddingBottom: 6, borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
                Context Info
              </div>
              {readonlyKeys.map(k => (
                <ReadonlyField key={k} label={k.replace(/_/g, ' ')} value={item.readonly_data?.[k]} />
              ))}
            </div>
          )}

          {editableKeys.length > 0 && (
            <div style={{ marginBottom: 20 }}>
              <div style={{ fontSize: 11, color: '#00b4d8', fontFamily: 'var(--font-mono)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 10, paddingBottom: 6, borderBottom: '1px solid rgba(0,180,216,0.1)' }}>
                Review &amp; Edit
              </div>
              {editableKeys.map(k => (
                <EditableField
                  key={k}
                  label={k.replace(/_/g, ' ')}
                  value={editedValues[k] ?? ''}
                  onChange={val => setEditedValues(prev => ({ ...prev, [k]: val }))}
                />
              ))}
            </div>
          )}

          <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)' }}>
            <button
              onClick={handleReject}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 16px',
                background: loading === 'reject' ? 'rgba(239,68,68,0.15)' : 'rgba(239,68,68,0.1)',
                border: '1px solid rgba(239,68,68,0.3)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#ef4444', fontSize: 13, fontWeight: 500,
                opacity: loading && loading !== 'reject' ? 0.5 : 1,
              }}
            >
              <XCircle size={14} />
              {loading === 'reject' ? 'Rejecting…' : isBrowserSite(item) ? 'Not now' : 'Reject'}
            </button>
            <button
              onClick={handleApprove}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 18px',
                background: loading === 'approve' ? 'rgba(16,185,129,0.25)' : 'rgba(16,185,129,0.15)',
                border: '1px solid rgba(16,185,129,0.4)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#10b981', fontSize: 13, fontWeight: 600,
                opacity: loading && loading !== 'approve' ? 0.5 : 1,
              }}
            >
              <CheckCircle size={14} />
              {loading === 'approve' ? 'Approving…' : isBrowserSite(item) ? 'Allow' : 'Approve & Continue'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// ── 2. Draft Outbound Message Card ────────────────────────────
function DraftMessageCard({ item, onSend, onReject, onProfile, onNavigate }) {
  const [expanded, setExpanded] = useState(true)
  const [loading, setLoading] = useState(null) // 'send' | 'reject'

  const handleSend = async () => {
    setLoading('send')
    try { await onSend(item.id) } finally { setLoading(null) }
  }
  const handleReject = async () => {
    setLoading('reject')
    try { await onReject(item.id) } finally { setLoading(null) }
  }

  return (
    <div style={{
      background: 'linear-gradient(160deg,#1c1608 0%,#120d05 100%)',
      border: '1.5px solid rgba(245,158,11,0.25)',
      borderRadius: 10,
      marginBottom: 16,
      overflow: 'hidden',
    }}>
      <div
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '12px 16px',
          background: 'rgba(245,158,11,0.08)',
          borderBottom: expanded ? '1px solid rgba(245,158,11,0.15)' : 'none',
          cursor: 'pointer',
        }}
        onClick={() => setExpanded(v => !v)}
      >
        <Mail size={14} color="#f59e0b" style={{ flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>
            {item.subject || '(no subject)'}
            <span style={{ fontWeight: 400, color: 'var(--text-muted)', marginLeft: 6 }}>
              — to {item.person_full_name || item.person_platform_username}
            </span>
          </div>
          <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
            {formatDate(item.created_at)} · {item.source}
          </div>
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(245,158,11,0.15)', color: '#f59e0b',
          fontSize: 11, fontFamily: 'var(--font-mono)', flexShrink: 0,
        }}>draft</span>
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      {(item.person_id || item.hil_ref) && (
        <div style={{ padding: '6px 16px' }}>
          <CommsLinks personId={item.person_id} hilRef={item.hil_ref || ''} name={item.person_full_name || item.person_platform_username} onProfile={onProfile} onNavigate={onNavigate} />
        </div>
      )}
      {expanded && (
        <div style={{ padding: 16 }}>
          <div style={{
            padding: '9px 12px', background: 'rgba(255,255,255,0.04)', borderRadius: 6,
            border: '1px solid rgba(255,255,255,0.07)', fontSize: 13, color: 'var(--text-secondary)',
            whiteSpace: 'pre-wrap', marginBottom: 16,
          }}>
            {item.body}
          </div>

          <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)' }}>
            <button
              onClick={handleReject}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 16px',
                background: loading === 'reject' ? 'rgba(239,68,68,0.15)' : 'rgba(239,68,68,0.1)',
                border: '1px solid rgba(239,68,68,0.3)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#ef4444', fontSize: 13, fontWeight: 500,
                opacity: loading && loading !== 'reject' ? 0.5 : 1,
              }}
            >
              <XCircle size={14} />
              {loading === 'reject' ? 'Discarding…' : 'Discard'}
            </button>
            <button
              onClick={handleSend}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 18px',
                background: loading === 'send' ? 'rgba(16,185,129,0.25)' : 'rgba(16,185,129,0.15)',
                border: '1px solid rgba(16,185,129,0.4)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#10b981', fontSize: 13, fontWeight: 600,
                opacity: loading && loading !== 'send' ? 0.5 : 1,
              }}
            >
              <CheckCircle size={14} />
              {loading === 'send' ? 'Sending…' : 'Send'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// ── 3. Lead / Candidate Outreach Approval Card ────────────────
function LeadApprovalCard({ item, onApprove, onReject, onOpenProfile }) {
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

// ── 4. Org Decision / Question / Gate Card ────────────────────
function OrgDecisionCard({ item, onAnswerQuestion, onApproveAction, onDenyAction, onApproveGate, onRejectGate }) {
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
        await onApproveAction(item.orgName, item.role, item.action)
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
        await onDenyAction(item.orgName, item.role, item.action)
      } else if (item.kind === 'gate') {
        await onRejectGate(item.orgName, item.id)
      }
    } finally {
      setLoading(null)
    }
  }

  const KindIcon = item.kind === 'question' ? MessageCircleQuestion : item.kind === 'gate' ? ShieldAlert : KeyRound

  return (
    <div style={{
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
            {item.text || item.summary || item.question || item.description}
          </div>

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

// ── Main HumanInLoop component ────────────────────────────────
export default function HumanInLoop({ embedded = false, isOpen = true, onClose, onPendingCountChange, onProfile, onNavigate }) {
  const [items, setItems] = useState([])
  const [drafts, setDrafts] = useState([])
  const [leads, setLeads] = useState([])
  const [orgItems, setOrgItems] = useState([])
  const [activeFilter, setActiveFilter] = useState('all') // 'all' | 'leads' | 'workflows' | 'drafts' | 'orgs'
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const pageVisibleRef = usePageVisibleRef()
  // The items that wait for the person (a giant plan): one poll, held by App.
  const { items: personItems } = usePersonItemsContext()
  const baseTotal = useRef(0)
  const personCount = useRef(0)
  personCount.current = personItems.length

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true)
    setError(null)
    try {
      const [hilRes, draftRes, leadRes, orgsRes] = await Promise.allSettled([
        GetHILItems(),
        GetDraftPersonMessages(),
        api.getPendingPeopleApprovals(),
        api.listOrgDesigns(),
      ])

      const newHil = (hilRes.status === 'fulfilled' && Array.isArray(hilRes.value)) ? hilRes.value : []
      const newDrafts = (draftRes.status === 'fulfilled' && Array.isArray(draftRes.value)) ? draftRes.value : []
      const newLeads = (leadRes.status === 'fulfilled' && Array.isArray(leadRes.value)) ? leadRes.value : []

      let newOrgItems = []
      if (orgsRes.status === 'fulfilled' && orgsRes.value) {
        const orgList = Array.isArray(orgsRes.value.items)
          ? orgsRes.value.items
          : (Array.isArray(orgsRes.value) ? orgsRes.value : [])
        const orgPromises = orgList.map(async (org) => {
          const orgName = org.name
          if (!orgName) return []
          try {
            const [qRes, aRes, gRes] = await Promise.allSettled([
              api.getOrgQuestions(orgName),
              api.getOrgApprovals(orgName),
              api.getOrgGates(orgName),
            ])
            const questions = qRes.status === 'fulfilled' ? (qRes.value?.questions || []) : []
            const approvals = aRes.status === 'fulfilled' ? (aRes.value?.approvals || []) : []
            const gates = gRes.status === 'fulfilled' ? (gRes.value?.gates || []) : []

            const res = []
            for (const q of questions) {
              if (q.answer == null && !q.answeredAt) {
                res.push({ kind: 'question', id: q.questionId, orgName, role: q.role, question: q.question, text: q.question, raw: q })
              }
            }
            for (const a of approvals) {
              if (a.approved == null) {
                res.push({ kind: 'approval', id: a.requestId || `${a.roleId}:${a.action}`, orgName, role: a.roleId, action: a.action, summary: a.question || `Approve ${a.action}?`, text: a.question || `Approve ${a.action}?`, raw: a })
              }
            }
            for (const g of gates) {
              if (g.status === 'pending') {
                res.push({ kind: 'gate', id: g.id, orgName, role: g.roleId, name: g.name, text: g.name || g.id, raw: g })
              }
            }
            return res
          } catch {
            return []
          }
        })
        const orgResults = await Promise.all(orgPromises)
        newOrgItems = orgResults.flat()
      }

      setItems(sortBySuggestion(newHil))
      setDrafts(newDrafts)
      setLeads(newLeads)
      setOrgItems(newOrgItems)

      baseTotal.current = newHil.length + newDrafts.length + newLeads.length + newOrgItems.length
      onPendingCountChange?.(baseTotal.current + personCount.current)
    } catch (e) {
      setError(e?.message ?? 'Failed to load')
    } finally {
      if (!background) setLoading(false)
    }
  }, [onPendingCountChange])

  useVisibleCatchUp(() => load(true))
  useEffect(() => { onPendingCountChange?.(baseTotal.current + personItems.length) }, [personItems.length, onPendingCountChange])

  useEffect(() => {
    if (embedded && !isOpen) return
    load()
    const interval = setInterval(() => {
      if (!pageVisibleRef.current) return
      load(true)
    }, 4000)
    return () => clearInterval(interval)
  }, [load, pageVisibleRef, embedded, isOpen])

  // Workflow HIL resolution
  const handleApproveHIL = async (id, editedJSON) => {
    try {
      await ApproveHIL(id, editedJSON)
      emitCommsChanged()
      setItems(prev => prev.filter(i => i.id !== id))
      notify('workflow', 'Workflow resumed')
    } catch (e) {
      setError(e?.message ?? 'Failed to approve item')
    }
  }

  const handleRejectHIL = async (id) => {
    try {
      await RejectHIL(id)
      emitCommsChanged()
      setItems(prev => prev.filter(i => i.id !== id))
      notify('workflow', 'Workflow branch rejected')
    } catch (e) {
      setError(e?.message ?? 'Failed to reject item')
    }
  }

  // Draft Message resolution
  const handleSendDraft = async (id) => {
    try {
      await SendDraftPersonMessage(id)
      emitCommsChanged()
      setDrafts(prev => prev.filter(d => d.id !== id))
      notify('message', 'Draft message sent')
    } catch (e) {
      setError(e?.message ?? 'Failed to send draft')
    }
  }

  const handleRejectDraft = async (id) => {
    try {
      await RejectDraftPersonMessage(id)
      emitCommsChanged()
      setDrafts(prev => prev.filter(d => d.id !== id))
      notify('message', 'Draft message discarded')
    } catch (e) {
      setError(e?.message ?? 'Failed to discard draft')
    }
  }

  // Lead outreach resolution
  const handleApproveLead = async (id, editedIntro, sendNow) => {
    try {
      await api.approvePendingPerson(id, editedIntro, sendNow)
      emitCommsChanged()
      setLeads(prev => prev.filter(l => l.id !== id))
      notify('outreach', sendNow ? 'Approved and queued for dispatch' : 'Approved candidate')
    } catch (e) {
      setError(e?.message ?? 'Failed to approve lead')
    }
  }

  const handleRejectLead = async (id) => {
    try {
      await api.rejectPendingPerson(id)
      emitCommsChanged()
      setLeads(prev => prev.filter(l => l.id !== id))
      notify('outreach', 'Candidate rejected')
    } catch (e) {
      setError(e?.message ?? 'Failed to reject lead')
    }
  }

  // Org resolution
  const handleAnswerQuestion = async (orgName, questionId, text) => {
    try {
      await api.answerOrgQuestion(orgName, questionId, text)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.id === questionId)))
      notify('org', 'Question answered')
    } catch (e) {
      setError(e?.message ?? 'Failed to answer question')
    }
  }

  const handleApproveAction = async (orgName, role, action) => {
    try {
      await api.approveOrgAction(orgName, role, action)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.role === role && i.action === action)))
      notify('org', `Approved ${action}`)
    } catch (e) {
      setError(e?.message ?? 'Failed to approve action')
    }
  }

  const handleDenyAction = async (orgName, role, action) => {
    try {
      await api.denyOrgAction(orgName, role, action)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.role === role && i.action === action)))
      notify('org', `Denied ${action}`)
    } catch (e) {
      setError(e?.message ?? 'Failed to deny action')
    }
  }

  const handleApproveGate = async (orgName, gateId) => {
    try {
      await api.gateApproveOrgAction(orgName, gateId)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.id === gateId)))
      notify('org', 'Gate approved')
    } catch (e) {
      setError(e?.message ?? 'Failed to approve gate')
    }
  }

  const handleRejectGate = async (orgName, gateId) => {
    try {
      await api.gateRejectOrgAction(orgName, gateId)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.id === gateId)))
      notify('org', 'Gate rejected')
    } catch (e) {
      setError(e?.message ?? 'Failed to reject gate')
    }
  }

  if (embedded && !isOpen) return null

  const totalCount = items.length + drafts.length + leads.length + orgItems.length + personItems.length

  const showLeads = activeFilter === 'all' || activeFilter === 'leads'
  const showWorkflows = activeFilter === 'all' || activeFilter === 'workflows'
  const showDrafts = activeFilter === 'all' || activeFilter === 'drafts'
  const showOrgs = activeFilter === 'all' || activeFilter === 'orgs'

  const containerStyle = embedded
    ? {
        width: 480,
        maxWidth: '100vw',
        flexShrink: 0,
        background: '#060b13',
        borderLeft: '1px solid rgba(0,180,216,0.15)',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
        position: 'relative',
        height: '100%',
      }
    : { padding: '24px 28px', maxWidth: 800, margin: '0 auto', overflowY: 'auto', height: '100%' }

  const bodyStyle = embedded
    ? { flex: 1, overflowY: 'auto', padding: '16px 16px 24px' }
    : undefined

  const body = (
    <>
      {!embedded && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
          <div>
            <h1 style={{ fontSize: 20, fontWeight: 700, color: '#e2e8f0', margin: 0 }}>Human in Loop</h1>
            <p style={{ fontSize: 13, color: 'var(--text-muted)', margin: '4px 0 0' }}>
              Central queue for all pending human reviews, lead outreach approvals, workflow pauses, and agent decisions.
            </p>
          </div>
          <button
            onClick={() => load()}
            disabled={loading}
            style={{
              marginLeft: 'auto',
              display: 'flex', alignItems: 'center', gap: 6,
              padding: '7px 14px',
              background: 'rgba(255,255,255,0.06)',
              border: '1px solid rgba(255,255,255,0.1)',
              borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
              color: 'var(--text-secondary)', fontSize: 13,
            }}
          >
            <RefreshCw size={13} style={{ animation: loading ? 'spin 1s linear infinite' : 'none' }} />
            Refresh
          </button>
        </div>
      )}

      <PersonItemsInbox />

      {/* Filter Tabs / Pills */}
      {totalCount > 0 && (
        <div style={{ display: 'flex', gap: 6, marginBottom: 16, overflowX: 'auto', paddingBottom: 4 }}>
          <button
            onClick={() => setActiveFilter('all')}
            style={{
              padding: '4px 10px', borderRadius: 20,
              fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
              background: activeFilter === 'all' ? 'rgba(0,180,216,0.2)' : 'rgba(255,255,255,0.04)',
              border: activeFilter === 'all' ? '1px solid rgba(0,180,216,0.4)' : '1px solid rgba(255,255,255,0.08)',
              color: activeFilter === 'all' ? '#00b4d8' : 'var(--text-muted)',
              cursor: 'pointer',
            }}
          >
            All ({totalCount})
          </button>
          {leads.length > 0 && (
            <button
              onClick={() => setActiveFilter('leads')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'leads' ? 'rgba(0,180,216,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'leads' ? '1px solid rgba(0,180,216,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'leads' ? '#00b4d8' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Leads ({leads.length})
            </button>
          )}
          {items.length > 0 && (
            <button
              onClick={() => setActiveFilter('workflows')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'workflows' ? 'rgba(0,180,216,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'workflows' ? '1px solid rgba(0,180,216,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'workflows' ? '#00b4d8' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Workflows ({items.length})
            </button>
          )}
          {drafts.length > 0 && (
            <button
              onClick={() => setActiveFilter('drafts')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'drafts' ? 'rgba(245,158,11,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'drafts' ? '1px solid rgba(245,158,11,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'drafts' ? '#f59e0b' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Messages ({drafts.length})
            </button>
          )}
          {orgItems.length > 0 && (
            <button
              onClick={() => setActiveFilter('orgs')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'orgs' ? 'rgba(139,92,246,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'orgs' ? '1px solid rgba(139,92,246,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'orgs' ? '#a78bfa' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Orgs ({orgItems.length})
            </button>
          )}
        </div>
      )}

      {error && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 14px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 8, marginBottom: 16, color: '#ef4444', fontSize: 13 }}>
          <AlertTriangle size={14} /> {error}
        </div>
      )}

      {!loading && totalCount === 0 && !error && (
        <div style={{
          textAlign: 'center', padding: '60px 20px',
          border: '1px dashed rgba(255,255,255,0.1)', borderRadius: 12,
          color: 'var(--text-muted)',
        }}>
          <CheckCircle size={32} style={{ marginBottom: 12, opacity: 0.3 }} />
          <div style={{ fontSize: 15, fontWeight: 500, marginBottom: 6 }}>All caught up!</div>
          <div style={{ fontSize: 13 }}>
            No pending reviews across lead campaigns, workflow pauses, outbound drafts, or agent organizations.
          </div>
        </div>
      )}

      {/* Lead Approvals */}
      {showLeads && leads.map(item => (
        <LeadApprovalCard
          key={item.id}
          item={item}
          onApprove={handleApproveLead}
          onReject={handleRejectLead}
          onOpenProfile={onProfile}
        />
      ))}

      {/* Workflow HIL Pauses */}
      {showWorkflows && items.map(item => (
        <HILCard
          key={item.id}
          item={item}
          onApprove={handleApproveHIL}
          onReject={handleRejectHIL}
          onProfile={onProfile}
          onNavigate={onNavigate}
        />
      ))}

      {/* Draft Messages */}
      {showDrafts && drafts.map(item => (
        <DraftMessageCard
          key={item.id}
          item={item}
          onSend={handleSendDraft}
          onReject={handleRejectDraft}
          onProfile={onProfile}
          onNavigate={onNavigate}
        />
      ))}

      {/* Org Decisions */}
      {showOrgs && orgItems.map(item => (
        <OrgDecisionCard
          key={`${item.orgName}-${item.kind}-${item.id}`}
          item={item}
          onAnswerQuestion={handleAnswerQuestion}
          onApproveAction={handleApproveAction}
          onDenyAction={handleDenyAction}
          onApproveGate={handleApproveGate}
          onRejectGate={handleRejectGate}
        />
      ))}

      <style>{`@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }`}</style>
    </>
  )

  if (!embedded) return <div style={containerStyle}>{body}</div>

  return (
    <div style={containerStyle}>
      <div style={{ padding: '10px 14px', borderBottom: '1px solid rgba(0,180,216,0.12)', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
        <UserCheck size={14} style={{ color: '#00b4d8' }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700, color: '#e2e8f0', flex: 1, letterSpacing: 1 }}>
          HUMAN IN LOOP
        </span>
        {totalCount > 0 && (
          <span style={{
            padding: '1px 6px', borderRadius: 10,
            background: 'rgba(239,68,68,0.2)', color: '#ef4444',
            fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700,
          }}>
            {totalCount}
          </span>
        )}
        <button onClick={() => load()} disabled={loading} title="Refresh" style={{ background: 'transparent', border: 'none', cursor: loading ? 'wait' : 'pointer', color: 'var(--text-muted)', padding: 3, display: 'flex' }}>
          <RefreshCw size={13} style={{ animation: loading ? 'spin 1s linear infinite' : 'none' }} />
        </button>
        {onClose && (
          <button onClick={onClose} title="Close panel" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 3, display: 'flex' }}>
            <X size={15} />
          </button>
        )}
      </div>
      <div style={bodyStyle}>{body}</div>
    </div>
  )
}
