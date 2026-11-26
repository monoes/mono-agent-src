import { Workflow, Building2, FileText, Image as ImageIcon, Copy, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import i18nOr from '../../lib/i18nOr.js'

function copyToClipboard(text) {
  try { navigator.clipboard?.writeText(text) } catch { /* clipboard unavailable — copy is a convenience, not required */ }
}

function fmtBytes(b) {
  if (!b || b < 1024) return `${b || 0} B`
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
  return `${(b / 1024 / 1024).toFixed(1)} MB`
}

const cardStyle = {
  display: 'flex', alignItems: 'center', gap: 8,
  background: '#020509',
  border: '1px solid rgba(0,180,216,0.12)',
  borderRadius: 8,
  marginTop: 6,
  padding: '7px 10px',
}
const labelStyle = {
  fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0',
  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
}
const subStyle = {
  fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)',
  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
}
const actionBtnStyle = {
  display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0,
  background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
  borderRadius: 6, padding: '4px 8px', cursor: 'pointer',
  color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 10,
}

// ChatArtifactCard renders a small, additive action for a chat result
// already confirmed real by chatArtifacts.resolveArtifact — it never
// replaces the generic ToolActivityCard for the same tool call (plan:
// "Keep generic tool output as fallback"), it only adds a click-to-open
// action beneath it (a workflow also keeps a local Copy ID).
// `onOpenArtifact` is not invoked for Copy ID — a pure clipboard copy.
export function ChatArtifactCard({ artifact, onOpenArtifact }) {
  useTranslation() // renders again on a language change
  if (!artifact) return null

  if (artifact.type === 'workflow') {
    return (
      <div style={cardStyle}>
        <Workflow size={12} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <div style={labelStyle}>{artifact.name}</div>
          <div style={subStyle}>{artifact.id}</div>
        </div>
        <button
          type="button"
          onClick={() => onOpenArtifact?.(artifact)}
          title={i18nOr('ui7Chat.open_workflow')}
          aria-label={i18nOr('ui7Chat.open_workflow')}
          style={actionBtnStyle}
        >
          <ExternalLink size={11} /> {i18nOr('ui7Chat.open')}
        </button>
        <button
          type="button"
          onClick={() => copyToClipboard(artifact.id)}
          title={i18nOr('ui7Chat.copy_workflow_id')}
          aria-label={i18nOr('ui7Chat.copy_workflow_id')}
          style={actionBtnStyle}
        >
          <Copy size={11} /> {i18nOr('ui7Chat.copy_id')}
        </button>
      </div>
    )
  }

  if (artifact.type === 'org') {
    return (
      <div style={cardStyle}>
        <Building2 size={12} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <div style={labelStyle}>{artifact.name}</div>
        </div>
        <button
          type="button"
          onClick={() => onOpenArtifact?.(artifact)}
          title={i18nOr('ui7Chat.open_org')}
          aria-label={i18nOr('ui7Chat.open_org')}
          style={actionBtnStyle}
        >
          <ExternalLink size={11} /> {i18nOr('ui7Chat.open')}
        </button>
      </div>
    )
  }

  if (artifact.type === 'document') {
    return (
      <div style={cardStyle}>
        <FileText size={12} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <div style={labelStyle}>{artifact.filename}</div>
          <div style={subStyle}>{fmtBytes(artifact.sizeBytes)}</div>
        </div>
        <button
          type="button"
          onClick={() => onOpenArtifact?.(artifact)}
          title={i18nOr('ui7Chat.open_document')}
          aria-label={i18nOr('ui7Chat.open_document')}
          style={actionBtnStyle}
        >
          <ExternalLink size={11} /> {i18nOr('ui7Chat.open')}
        </button>
      </div>
    )
  }

  if (artifact.type === 'image') {
    return (
      <div style={cardStyle}>
        <ImageIcon size={12} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <div style={labelStyle}>{artifact.label || artifact.filename}</div>
          <div style={subStyle}>{artifact.filename || artifact.id}</div>
        </div>
        <button
          type="button"
          onClick={() => onOpenArtifact?.(artifact)}
          title={i18nOr('ui7Chat.open_image')}
          aria-label={i18nOr('ui7Chat.open_image')}
          style={actionBtnStyle}
        >
          <ExternalLink size={11} /> {i18nOr('ui7Chat.open')}
        </button>
      </div>
    )
  }

  return null
}
