import { useTranslation } from 'react-i18next'
import { MessageSquare, Building2, Link2, ExternalLink } from 'lucide-react'
import { relativeTime, orgRunLabel } from '../../lib/historyModel.js'

// One session in the history list: a chat or an org run. The row's main
// button opens it; the checkbox (chats only) selects it for deletion; the
// application link goes to that application.
export default function HistoryRow({ row, live, mission, selected, onToggle, onOpen, onOpenOrgs, onOpenApplication }) {
  const { t } = useTranslation()
  const isOrg = row.kind === 'org'
  const title = row.title || (isOrg ? row.orgName : t('history.kindChatIcon'))
  const statusText = t(`history.status_${live.status}`, { defaultValue: live.status })
  const runtime = [row.runtime, row.model].filter(Boolean).join(' / ')
  return (
    <li className="hist-row" data-status={live.status} data-kind={row.kind} data-selected={selected ? 'true' : undefined}>
      {isOrg
        ? <span className="hist-check-gap" aria-hidden="true" />
        : (
          <input
            type="checkbox"
            className="hist-check"
            checked={selected}
            onChange={() => onToggle(row.id)}
            aria-label={t('history.select', { title })}
          />
        )}
      <button type="button" className="hist-main" onClick={() => onOpen(row)}>
        <span className="hist-kind" role="img" aria-label={t(isOrg ? 'history.kindOrgIcon' : 'history.kindChatIcon')}>
          {isOrg ? <Building2 size={14} aria-hidden="true" /> : <MessageSquare size={14} aria-hidden="true" />}
        </span>
        <span className="hist-body">
          <span className="hist-title">{title}</span>
          <span className="hist-meta">
            {runtime && <span>{runtime}</span>}
            {mission && <span className="hist-access" data-testid="hist-mission" data-mission={mission}>{t('history.mission', { state: t(`tasks.session.state.${mission}`, { defaultValue: mission }) })}</span>}
            {row.access && <span className="hist-access" data-access={row.access}>{t(`history.access_${row.access}`, { defaultValue: row.access })}</span>}
            {isOrg && row.orgName && row.orgName !== title && <span>{row.orgName}</span>}
            {isOrg && (row.runId || row.updatedAt) && <span className="hist-run">{orgRunLabel(row)}</span>}
          </span>
        </span>
        <span className="hist-side">
          <span className="hist-status">
            {live.unread > 0 && <span className="hist-unread" aria-label={t('history.unread', { count: live.unread })}>{live.unread}</span>}
            <span className="hist-dot" role="img" aria-label={statusText} title={statusText} />
          </span>
          <time className="hist-time" dateTime={row.updatedAt}>{relativeTime(row.updatedAt)}</time>
        </span>
      </button>
      {isOrg && onOpenOrgs && (
        <button type="button" className="hist-app" onClick={() => onOpenOrgs(row)} aria-label={t('history.openInOrgs', { name: row.orgName })} title={t('history.openInOrgs', { name: row.orgName })}>
          <ExternalLink size={11} aria-hidden="true" />
          <span>{t('history.openInOrgsShort')}</span>
        </button>
      )}
      {row.applicationId && (
        <button type="button" className="hist-app" onClick={() => onOpenApplication(row.applicationId)} aria-label={t('history.openApplication')} title={t('history.openApplication')}>
          <Link2 size={11} aria-hidden="true" />
          <span>{row.taskGoal || t('history.applicationLink', { id: row.applicationId })}</span>
        </button>
      )}
    </li>
  )
}
