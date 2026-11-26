import React from 'react'
import { useTranslation } from 'react-i18next'
import { ListTodo, CheckCircle2, Clock, Circle } from 'lucide-react'
import { sectionCardStyle, sectionTitleStyle, subtleBtnStyle, statusWord } from './constants.jsx'
import t from '../../lib/i18nOr.js'

const TASK_STATUS_KEYS = {
  todo: 'ui6PagesApps.task_todo',
  inbox: 'ui6PagesApps.task_inbox',
  ready: 'ui6PagesApps.task_ready',
  in_progress: 'ui6PagesApps.task_in_progress',
  review: 'ui6PagesApps.task_review',
  running: 'ui6PagesApps.task_running',
  done: 'ui6PagesApps.task_done',
  completed: 'ui6PagesApps.task_completed',
}

function TaskStatusBadge({ status }) {
  useTranslation()
  const norm = (status || 'todo').toLowerCase()
  let bg = 'rgba(100,116,139,0.15)'
  let color = '#94a3b8'

  if (norm === 'done' || norm === 'completed') {
    bg = 'rgba(16,185,129,0.15)'
    color = '#10b981'
  } else if (norm === 'in_progress' || norm === 'running') {
    bg = 'rgba(0,180,216,0.15)'
    color = '#00b4d8'
  } else if (norm === 'ready') {
    bg = 'rgba(245,158,11,0.15)'
    color = '#f59e0b'
  }

  return (
    <span
      role="status"
      aria-label={t('ui6PagesApps.task_status_label', { status: norm === 'in_progress' ? t('ui6PagesApps.task_in_progress_raw') : Object.hasOwn(TASK_STATUS_KEYS, norm) ? t(TASK_STATUS_KEYS[norm]) : norm })}
      style={{
        background: bg,
        border: `1px solid ${color}33`,
        borderRadius: 4,
        padding: '1px 6px',
        fontSize: 10,
        fontWeight: 600,
        color,
        fontFamily: 'var(--font-mono)',
        textTransform: 'uppercase',
      }}
    >
      {statusWord(TASK_STATUS_KEYS, norm)}
    </span>
  )
}

export default function ApplicationTasksSection({ tasks = [], onNavigate }) {
  useTranslation()
  const taskList = Array.isArray(tasks) ? tasks : []

  const handleOpenTask = (task) => {
    if (typeof onNavigate === 'function') {
      onNavigate('tasks', { taskId: task.id })
    }
  }

  return (
    <section aria-labelledby="section-tasks-title" style={sectionCardStyle}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <h3 id="section-tasks-title" style={sectionTitleStyle}>
          <ListTodo size={14} style={{ color: '#00b4d8' }} />
          {t('ui6PagesApps.tasks_n', { n: taskList.length })}
        </h3>
        <span style={{ fontSize: 11, color: 'var(--text-muted, #64748b)', fontFamily: 'var(--font-mono)' }}>
          {t('ui6PagesApps.board_items')}
        </span>
      </div>

      {taskList.length === 0 ? (
        <div
          style={{
            padding: '12px 14px',
            borderRadius: 6,
            background: 'rgba(0,0,0,0.2)',
            color: 'var(--text-muted, #64748b)',
            fontSize: 12,
            fontFamily: 'var(--font-mono)',
            textAlign: 'center',
          }}
        >
          {t('ui6PagesApps.no_tasks')}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {taskList.map((task, idx) => (
            <div
              key={task.id || idx}
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                gap: 12,
                padding: '8px 12px',
                borderRadius: 6,
                background: 'rgba(0,0,0,0.25)',
                border: '1px solid rgba(255,255,255,0.05)',
                fontFamily: 'var(--font-mono)',
                fontSize: 11,
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0, flex: 1 }}>
                <Circle size={10} style={{ color: '#00b4d8', flexShrink: 0 }} />
                <span
                  style={{
                    color: 'var(--text-primary, #f1f5f9)',
                    fontWeight: 500,
                    wordBreak: 'break-word',
                  }}
                >
                  {task.title || t('ui6PagesApps.untitled_task')}
                </span>
                <TaskStatusBadge status={task.status} />
              </div>

              {onNavigate && (
                <button
                  type="button"
                  onClick={() => handleOpenTask(task)}
                  style={subtleBtnStyle}
                  aria-label={t('ui6PagesApps.open_board_for', { name: task.title || t('ui6PagesApps.task_word') })}
                >
                  {t('ui6PagesApps.view')}
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
