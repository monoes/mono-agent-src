import React, { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FileText, ExternalLink, Calendar, ChevronDown, ChevronUp } from 'lucide-react'
import * as WailsApp from '../../wailsjs/go/main/App'
import { sectionCardStyle, sectionTitleStyle, subtleBtnStyle } from './constants.jsx'
import t from '../../lib/i18nOr.js'

export default function ApplicationPostingSection({ posting }) {
  useTranslation()
  const [expanded, setExpanded] = useState(false)

  const url = posting?.url
  const capturedText = posting?.capturedText?.trim()
  const capturedAt = posting?.capturedAt
  const deadline = posting?.deadline

  const handleOpenUrl = (e) => {
    e.preventDefault()
    if (url) {
      if (typeof WailsApp.OpenURL === 'function') {
        WailsApp.OpenURL(url)
      } else {
        window.open(url, '_blank', 'noopener,noreferrer')
      }
    }
  }

  const isLong = (capturedText?.length || 0) > 400

  return (
    <section aria-labelledby="section-posting-title" style={sectionCardStyle}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
        <h3 id="section-posting-title" style={sectionTitleStyle}>
          <FileText size={14} style={{ color: '#00b4d8' }} />
          {t('ui6PagesApps.posting')}
        </h3>

        <div style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 11, fontFamily: 'var(--font-mono)' }}>
          {capturedAt && (
            <span style={{ color: 'var(--text-muted, #64748b)' }}>
              {t('ui6PagesApps.captured', { date: capturedAt.slice(0, 16).replace('T', ' ') })}
            </span>
          )}

          {url && (
            <a
              href={url}
              onClick={handleOpenUrl}
              style={{
                color: '#00b4d8',
                display: 'inline-flex',
                alignItems: 'center',
                gap: 4,
                textDecoration: 'none',
              }}
              title={t('ui6PagesApps.open_posting_link')}
              aria-label={t('ui6PagesApps.open_posting')}
            >
              <ExternalLink size={11} />
              <span>{t('ui6PagesApps.posting_link')}</span>
            </a>
          )}
        </div>
      </div>

      {!capturedText ? (
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
          {t('ui6PagesApps.no_posting')}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div
            style={{
              padding: 12,
              borderRadius: 6,
              background: 'rgba(0,0,0,0.3)',
              border: '1px solid rgba(255,255,255,0.05)',
              fontFamily: 'var(--font-mono)',
              fontSize: 12,
              lineHeight: 1.55,
              color: 'var(--text-secondary, #cbd5e1)',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
              maxHeight: expanded ? 'none' : 160,
              overflowY: expanded ? 'visible' : 'hidden',
              position: 'relative',
            }}
          >
            {capturedText}
          </div>

          {isLong && (
            <button
              type="button"
              onClick={() => setExpanded(!expanded)}
              style={{
                alignSelf: 'flex-start',
                background: 'transparent',
                border: 'none',
                color: '#00b4d8',
                cursor: 'pointer',
                fontFamily: 'var(--font-mono)',
                fontSize: 11,
                display: 'inline-flex',
                alignItems: 'center',
                gap: 4,
                padding: '2px 0',
              }}
              aria-label={expanded ? t('ui6PagesApps.collapse_posting') : t('ui6PagesApps.expand_posting')}
            >
              {expanded ? (
                <>
                  <ChevronUp size={12} /> {t('ui6PagesApps.show_less')}
                </>
              ) : (
                <>
                  <ChevronDown size={12} /> {t('ui6PagesApps.show_full')}
                </>
              )}
            </button>
          )}
        </div>
      )}
    </section>
  )
}
