import React from 'react'
import { useTranslation } from 'react-i18next'
import { Bot, MessageSquare, RotateCcw, ExternalLink } from 'lucide-react'
import { sectionCardStyle, sectionTitleStyle, subtleBtnStyle, statusWord } from './constants.jsx'
import t from '../../lib/i18nOr.js'

const CODER_STATUS_KEYS = {
  none: 'ui6PagesApps.coder_none',
  complete: 'ui6PagesApps.coder_complete',
  completed: 'ui6PagesApps.coder_completed',
  running: 'ui6PagesApps.coder_running',
  queued: 'ui6PagesApps.coder_queued',
  needs_user: 'ui6PagesApps.coder_needs_user',
  failed: 'ui6PagesApps.coder_failed',
  cancelled: 'ui6PagesApps.coder_cancelled',
}

function CoderTaskStatusBadge({ status }) {
  useTranslation()
  const norm = (status || 'none').toLowerCase()
  let bg = 'rgba(100,116,139,0.15)'
  let color = '#94a3b8'

  if (norm === 'complete' || norm === 'completed') {
    bg = 'rgba(16,185,129,0.15)'
    color = '#10b981'
  } else if (norm === 'running') {
    bg = 'rgba(0,180,216,0.15)'
    color = '#00b4d8'
  } else if (norm === 'queued') {
    bg = 'rgba(245,158,11,0.15)'
    color = '#f59e0b'
  } else if (norm === 'needs_user') {
    bg = 'rgba(244,63,94,0.15)'
    color = '#f43f5e'
  } else if (norm === 'failed') {
    bg = 'rgba(239,68,68,0.15)'
    color = '#ef4444'
  } else if (norm === 'cancelled') {
    bg = 'rgba(100,116,139,0.15)'
    color = '#64748b'
  }

  return (
    <span
      role="status"
      aria-label={t('ui6PagesApps.coder_status_label', { status: statusWord(CODER_STATUS_KEYS, norm) })}
      style={{
        background: bg,
        border: `1px solid ${color}33`,
        borderRadius: 4,
        padding: '2px 8px',
        fontSize: 10,
        fontWeight: 600,
        color,
        fontFamily: 'var(--font-mono)',
        textTransform: 'uppercase',
      }}
    >
      {statusWord(CODER_STATUS_KEYS, norm)}
    </span>
  )
}

const disabledActionBtnStyle = {
  background: 'rgba(255,255,255,0.03)',
  border: '1px solid rgba(255,255,255,0.08)',
  borderRadius: 5,
  padding: '5px 12px',
  color: 'var(--text-muted, #64748b)',
  fontFamily: 'var(--font-mono)',
  fontSize: 11,
  cursor: 'not-allowed',
  opacity: 0.6,
  display: 'inline-flex',
  alignItems: 'center',
  gap: 5,
}

export default function ApplicationCoderTaskSection({ task, onOpenCoderChat }) {
  useTranslation()
  const hasTask = Boolean(task)
  const conversationId = task?.conversationId
  const taskId = task?.taskId
  const canOpenChat = Boolean(conversationId)

  const handleOpenChat = () => {
    if (!canOpenChat) return
    const conv = typeof conversationId === 'object' ? conversationId : { id: conversationId }
    if (typeof onOpenCoderChat === 'function') {
      onOpenCoderChat(conv)
    }
  }

  return (
    <section aria-labelledby="section-coder-task-title" style={sectionCardStyle}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
        <h3 id="section-coder-task-title" style={sectionTitleStyle}>
          <Bot size={14} style={{ color: '#00b4d8' }} />
          {t('ui6PagesApps.coder_task')}
        </h3>

        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <CoderTaskStatusBadge status={hasTask ? task.status : 'none'} />
        </div>
      </div>

      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          gap: 10,
          padding: '10px 12px',
          borderRadius: 6,
          background: 'rgba(0,0,0,0.25)',
          border: '1px solid rgba(255,255,255,0.05)',
          fontFamily: 'var(--font-mono)',
          fontSize: 11,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            {taskId && (
              <span style={{ color: 'var(--text-muted, #64748b)' }}>
                {t('ui6PagesApps.task_id')} <span style={{ color: 'var(--text-primary, #f1f5f9)' }}>{taskId}</span>
              </span>
            )}

            {conversationId && (
              <span style={{ color: 'var(--text-muted, #64748b)' }}>
                {t('ui6PagesApps.conversation')} <span style={{ color: 'var(--text-primary, #f1f5f9)' }}>{conversationId}</span>
              </span>
            )}

            {!hasTask && (
              <span style={{ color: 'var(--text-muted, #64748b)' }}>
                {t('ui6PagesApps.no_coder_task')}
              </span>
            )}
          </div>
        </div>

        {/* Action buttons */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', paddingTop: 4 }}>
          {/* Open chat */}
          <button
            type="button"
            onClick={handleOpenChat}
            disabled={!canOpenChat}
            style={canOpenChat ? subtleBtnStyle : disabledActionBtnStyle}
            title={canOpenChat ? t('ui6PagesApps.open_chat_bubble') : t('ui6PagesApps.no_conversation')}
            aria-label={t('ui6PagesApps.open_chat_bubble')}
          >
            <MessageSquare size={12} />
            {t('ui6PagesApps.open_chat')}
          </button>

          {/* Reopen tab - disabled with tooltip */}
          <button
            type="button"
            disabled
            title={t('ui6PagesApps.coming_soon')}
            aria-label={t('ui6PagesApps.reopen_tab_soon')}
            style={disabledActionBtnStyle}
          >
            <ExternalLink size={12} />
            {t('ui6PagesApps.reopen_tab')}
          </button>

          {/* Refill - disabled with tooltip */}
          <button
            type="button"
            disabled
            title={t('ui6PagesApps.coming_soon')}
            aria-label={t('ui6PagesApps.refill_soon')}
            style={disabledActionBtnStyle}
          >
            <RotateCcw size={12} />
            {t('ui6PagesApps.refill')}
          </button>
        </div>
      </div>
    </section>
  )
}
