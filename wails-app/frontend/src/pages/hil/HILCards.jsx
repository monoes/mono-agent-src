import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import t from '../../lib/i18nOr.js'
import { CheckCircle, XCircle, Clock, ChevronDown, ChevronRight, Mail } from 'lucide-react'
import { CommsLinks } from '../../components/CommsLinks.jsx'
import { BrowserSubmitDetails, isBrowserSubmit } from '../../components/hil/BrowserSubmitDetails.jsx'
import { BrowserSiteDetails, isBrowserSite } from '../../components/hil/BrowserSiteDetails.jsx'
import { AutoDecideSkipped } from '../../components/hil/AutoDecideSkipped.jsx'
import { formatDate, ReadonlyField, EditableField } from './hilShared.jsx'

// The suggestion's values are the CLI's; only the displayed word is translated. An unknown
// value shows as it came.
const CHOICE_KEYS = { approve: 'ui7Hil.choice_approve', reject: 'ui7Hil.choice_reject', needs_human: 'ui7Hil.choice_needs_human' }
const RISK_KEYS = { low: 'ui7Hil.risk_low', medium: 'ui7Hil.risk_medium', high: 'ui7Hil.risk_high' }

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

// suggestionSource names who answered for the chip's tooltip: a suggestion's
// `model` is "<provider>/<model>" when a decision provider other than
// TypeSafe answered, else TypeSafe's model alone.
function suggestionSource(model) {
  if (typeof model !== 'string' || !model) return 'Jev'
  return model.includes('/') ? `Jev (${model})` : `TypeSafe Jev (${model})`
}

export function SuggestionChip({ choice, p, detail, model }) {
  useTranslation() // renders again on a language change
  if (!choice) return null
  const color = SUGGESTION_COLORS[choice] || '#94a3b8'
  const pct = typeof p === 'number' ? `${Math.round(p * 100)}%` : ''
  const label = Object.hasOwn(CHOICE_KEYS, choice) ? t(CHOICE_KEYS[choice]) : choice.replace(/_/g, ' ')
  return (
    <span
      data-testid="jev-suggestion"
      title={t('ui7Hil.suggest_title', { source: suggestionSource(model), label, confidence: pct ? ` ${t('ui7Hil.confidence', { pct })}` : '', detail: detail ? ` · ${detail}` : '' })}
      style={{
        padding: '2px 8px', borderRadius: 10, flexShrink: 0,
        background: `${color}1f`, color, border: `1px solid ${color}55`,
        fontSize: 11, fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap',
      }}
    >
      {t('ui7Hil.suggested')} {label}{pct && ` ${pct}`}{detail && ` · ${detail}`}
    </span>
  )
}

export function HILCard({ item, onApprove, onReject, onProfile, onNavigate }) {
  useTranslation() // renders again on a language change
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

  const risk = suggestionOf(item)?.risk

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
            {formatDate(item.created_at)} {t('ui7Hil.exec')} {item.execution_id?.slice(0, 8) ?? '—'}…
          </div>
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(0,180,216,0.12)', color: '#00b4d8',
          fontSize: 11, fontFamily: 'var(--font-mono)', flexShrink: 0,
        }}>{t(isBrowserSubmit(item) || isBrowserSite(item) ? 'ui7Hil.badge_browser' : 'ui7Hil.badge_workflow')}</span>
        {suggestionOf(item) && (
          <SuggestionChip
            choice={suggestionOf(item).choice}
            p={suggestionOf(item).p}
            detail={risk ? t('ui7Hil.risk', { level: Object.hasOwn(RISK_KEYS, risk) ? t(RISK_KEYS[risk]) : risk }) : ''}
            model={suggestionOf(item).model}
          />
        )}
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      <AutoDecideSkipped item={item} />

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
                {t('ui7Hil.context_info')}
              </div>
              {readonlyKeys.map(k => (
                <ReadonlyField key={k} label={k.replace(/_/g, ' ')} value={item.readonly_data?.[k]} />
              ))}
            </div>
          )}

          {editableKeys.length > 0 && (
            <div style={{ marginBottom: 20 }}>
              <div style={{ fontSize: 11, color: '#00b4d8', fontFamily: 'var(--font-mono)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 10, paddingBottom: 6, borderBottom: '1px solid rgba(0,180,216,0.1)' }}>
                {t('ui7Hil.review_edit')}
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
              {t(loading === 'reject' ? 'ui7Hil.rejecting' : isBrowserSite(item) ? 'ui7Hil.not_now' : 'ui7Hil.reject')}
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
              {t(loading === 'approve' ? 'ui7Hil.approving' : isBrowserSite(item) ? 'ui7Hil.allow' : 'ui7Hil.approve_continue')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}


// ── 2. Draft Outbound Message Card ────────────────────────────
export function DraftMessageCard({ item, onSend, onReject, onProfile, onNavigate }) {
  useTranslation() // renders again on a language change
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
            {item.subject || t('ui7Hil.no_subject')}
            <span style={{ fontWeight: 400, color: 'var(--text-muted)', marginLeft: 6 }}>
              {t('ui7Hil.to_recipient', { name: item.person_full_name || item.person_platform_username || '' })}
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
        }}>{t('ui7Hil.badge_draft')}</span>
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
              {t(loading === 'reject' ? 'ui7Hil.discarding' : 'ui7Hil.discard')}
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
              {t(loading === 'send' ? 'ui7Hil.sending' : 'ui7Hil.send')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
