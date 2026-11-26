import React from 'react'
import { useTranslation } from 'react-i18next'
import { ExternalLink, Clock, X, Building2, Calendar } from 'lucide-react'
import * as WailsApp from '../../wailsjs/go/main/App'
import { StatusBadge, subtleBtnStyle } from './constants.jsx'
import SendToCoderButton from '../SendToCoderButton.jsx'
import ApplyWithCoderButton from '../ApplyWithCoderButton.jsx'
import { selectionText } from '../SendToCoderApi.js'
import t from '../../lib/i18nOr.js'

export default function ApplicationHeader({ detail, assets, onSetStatus, onClose }) {
  useTranslation()
  if (!detail) return null

  const org = detail.kind === 'job' ? detail.company : (detail.issuing_org || detail.company)
  const title = detail.title || t('ui6PagesApps.untitled')
  const orgName = org || (detail.kind === 'job' ? t('ui6PagesApps.unknown_company') : t('ui6PagesApps.unknown_org'))
  // What the coder is told stays in English.
  const contextTitle = detail.title || 'Untitled Application'
  const contextOrg = org || (detail.kind === 'job' ? 'Unknown Company' : 'Unknown Organization')

  const deadline = detail.submission_deadline || detail.submissionDeadline || assets?.posting?.deadline
  const postingUrl = detail.url || assets?.posting?.url
  const canCancel = ['pending', 'awaiting_submit', 'needs_you'].includes(detail.status)

  const handleOpenUrl = (e) => {
    e.preventDefault()
    if (postingUrl) {
      if (typeof WailsApp.OpenURL === 'function') {
        WailsApp.OpenURL(postingUrl)
      } else {
        window.open(postingUrl, '_blank', 'noopener,noreferrer')
      }
    }
  }

  return (
    <header
      role="banner"
      aria-label={t('ui6PagesApps.header_label')}
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
        paddingBottom: 14,
        borderBottom: '1px solid rgba(255,255,255,0.08)',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0, flex: '1 1 240px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <h2
              style={{
                margin: 0,
                fontSize: 18,
                fontWeight: 600,
                color: 'var(--text-primary, #f1f5f9)',
                lineHeight: 1.3,
                wordBreak: 'break-word',
              }}
            >
              {title}
            </h2>
            <StatusBadge status={detail.status} />
          </div>

          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 12,
              flexWrap: 'wrap',
              fontSize: 12,
              color: 'var(--text-secondary, #94a3b8)',
              fontFamily: 'var(--font-mono)',
            }}
          >
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <Building2 size={13} style={{ color: '#64748b' }} />
              {orgName}
            </span>

            <span style={{ color: 'rgba(255,255,255,0.2)' }}>·</span>

            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <Clock size={13} style={{ color: '#64748b' }} />
              {deadline ? t('ui6PagesApps.deadline', { date: deadline }) : t('ui6PagesApps.no_deadline')}
            </span>

            {postingUrl && (
              <>
                <span style={{ color: 'rgba(255,255,255,0.2)' }}>·</span>
                <a
                  href={postingUrl}
                  onClick={handleOpenUrl}
                  title={t('ui6PagesApps.open_url')}
                  aria-label={t('ui6PagesApps.open_link', { url: postingUrl })}
                  style={{
                    color: '#00b4d8',
                    textDecoration: 'none',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 4,
                    maxWidth: 240,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  <ExternalLink size={12} />
                  <span>{postingUrl}</span>
                </a>
              </>
            )}
          </div>
        </div>

        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, flex: '0 1 auto', minWidth: 0, maxWidth: '100%', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
          {canCancel && <ApplyWithCoderButton applicationId={detail.id} />}
          <SendToCoderButton label={t('ui6PagesApps.discuss')} goal="Discuss this application with me: its fit, the posting, and what to do next."
            applicationId={detail.id} origin={{ page: 'applications', label: t('ui6PagesApps.applications') }}
            context={[selectionText([contextTitle, contextOrg, postingUrl].filter(Boolean).join(' | '))]} />
          {canCancel && onSetStatus && (
            <button
              type="button"
              onClick={() => onSetStatus(detail.id, 'cancelled')}
              style={{
                ...subtleBtnStyle,
                color: '#f43f5e',
                borderColor: 'rgba(244,63,94,0.3)',
                background: 'rgba(244,63,94,0.1)',
              }}
              aria-label={t('ui6PagesApps.cancel_this')}
            >
              {t('ui6PagesApps.cancel_application')}
            </button>
          )}

          {onClose && (
            <button
              type="button"
              onClick={onClose}
              aria-label={t('ui6PagesApps.close_detail')}
              style={{
                background: 'transparent',
                border: 'none',
                color: 'var(--text-muted, #64748b)',
                cursor: 'pointer',
                padding: 4,
                borderRadius: 4,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <X size={18} />
            </button>
          )}
        </div>
      </div>
    </header>
  )
}
