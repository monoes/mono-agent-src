import React, { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import ApplicationHeader from './ApplicationHeader.jsx'
import ApplicationParkedPanel from './ApplicationParkedPanel.jsx'
import ApplicationPostingSection from './ApplicationPostingSection.jsx'
import ApplicationDocumentsSection from './ApplicationDocumentsSection.jsx'
import ApplicationFillSection from './ApplicationFillSection.jsx'
import ApplicationPeopleMessagesSection from './ApplicationPeopleMessagesSection.jsx'
import ApplicationTasksSection from './ApplicationTasksSection.jsx'
import ApplicationStatusHistorySection from './ApplicationStatusHistorySection.jsx'
import ApplicationCoderTaskSection from './ApplicationCoderTaskSection.jsx'
import FileViewerModal from '../FileViewerModal.jsx'
import { fetchApplicationAssets } from './applicationAssets.js'
import t from '../../lib/i18nOr.js'

const FOCUSABLE = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])'

export default function ApplicationDetailModal({
  applicationId,
  detail,
  initialAssets = null,
  onClose,
  onSetStatus,
  onNavigate,
  onOpenCoderChat,
  onChanged,
}) {
  useTranslation()
  const overlayRef = useRef(null)
  const dialogRef = useRef(null)
  const [assets, setAssets] = useState(initialAssets)
  const [loadingAssets, setLoadingAssets] = useState(!initialAssets)
  const [previewDoc, setPreviewDoc] = useState(null)

  const id = applicationId || detail?.id

  // Close on Escape key press
  useEffect(() => {
    const handler = (e) => {
      if (e.key === 'Escape' && !previewDoc) {
        onClose?.()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose, previewDoc])

  // Take focus on open, keep Tab inside the dialog, and give focus back on close.
  useEffect(() => {
    const previous = document.activeElement
    dialogRef.current?.focus()
    return () => { if (previous?.isConnected) previous.focus?.() }
  }, [])

  const trapTab = (e) => {
    if (e.key !== 'Tab') return
    const items = [...dialogRef.current.querySelectorAll(FOCUSABLE)].filter(el => !el.disabled && getComputedStyle(el).display !== 'none' && getComputedStyle(el).visibility !== 'hidden')
    if (items.length === 0) { e.preventDefault(); return }
    const first = items[0]
    const last = items[items.length - 1]
    const active = document.activeElement
    if (e.shiftKey && (active === first || active === dialogRef.current)) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus() }
  }

  // Fetch assets safely with guard for missing binding
  useEffect(() => {
    if (initialAssets) {
      setAssets(initialAssets)
      setLoadingAssets(false)
      return
    }

    if (!id) {
      setLoadingAssets(false)
      return
    }

    let active = true
    setLoadingAssets(true)

    fetchApplicationAssets(id)
      .then((data) => {
        if (active) {
          setAssets(data)
          setLoadingAssets(false)
        }
      })
      .catch((err) => {
        if (active) {
          console.warn('Failed to fetch application assets:', err)
          setAssets(null)
          setLoadingAssets(false)
        }
      })

    return () => {
      active = false
    }
  }, [id, initialAssets])

  const handlePreviewDoc = (doc) => {
    if (!doc) return
    setPreviewDoc({
      id: doc.id,
      filename: doc.filename,
      path: doc.path,
      size_bytes: doc.sizeBytes != null ? doc.sizeBytes : doc.size_bytes,
    })
  }

  if (!detail && !id) return null

  const effectiveDetail = detail || { id, title: t('ui6PagesApps.detail_default_title'), status: 'pending' }

  return (
    <>
      <div
        ref={overlayRef}
        className="modal-overlay"
        onClick={(e) => {
          if (e.target === overlayRef.current) onClose?.()
        }}
        style={{
          position: 'fixed',
          inset: 0,
          zIndex: 9000,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: 'rgba(0,0,0,0.65)',
          padding: 16,
        }}
      >
        <div
          ref={dialogRef}
          role="dialog"
          aria-modal="true"
          aria-labelledby="application-detail-label"
          tabIndex={-1}
          onKeyDown={trapTab}
          style={{
            outline: 'none',
            background: 'var(--surface, #0d1520)',
            border: '1px solid rgba(255,255,255,0.1)',
            borderRadius: 10,
            padding: 20,
            width: 720,
            maxWidth: '95vw',
            maxHeight: '88vh',
            overflowY: 'auto',
            fontFamily: 'var(--font-mono)',
            display: 'flex',
            flexDirection: 'column',
            gap: 16,
            boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
          }}
        >
          <span id="application-detail-label" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap' }}>
            {t('ui6PagesApps.detail_label', { title: effectiveDetail.title || t('ui6PagesApps.application') })}
          </span>

          {/* Section 1: Header */}
          <ApplicationHeader
            detail={effectiveDetail}
            assets={assets}
            onSetStatus={onSetStatus}
            onClose={onClose}
          />

          <ApplicationParkedPanel detail={effectiveDetail} assets={assets} onChanged={onChanged} />

          {/* Section 2: Posting */}
          <ApplicationPostingSection posting={assets?.posting} />

          {/* Section 3: Documents */}
          <ApplicationDocumentsSection
            documents={assets?.documents}
            onPreviewDoc={handlePreviewDoc}
          />

          {/* Section 4: Filled Form */}
          <ApplicationFillSection fill={assets?.fill} />

          {/* Section 5: People & Messages */}
          <ApplicationPeopleMessagesSection
            people={assets?.people}
            messages={assets?.messages}
            onNavigate={onNavigate}
          />

          {/* Section 6: Tasks */}
          <ApplicationTasksSection
            tasks={assets?.tasks}
            onNavigate={onNavigate}
          />

          {/* Section 7: Status History */}
          <ApplicationStatusHistorySection
            statusLog={effectiveDetail.status_log || assets?.status_log}
          />

          {/* Section 8: Coder Task */}
          <ApplicationCoderTaskSection
            task={assets?.task}
            onOpenCoderChat={onOpenCoderChat}
          />

          {/* Modal Footer */}
          <div style={{ display: 'flex', justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)' }}>
            <button
              type="button"
              onClick={onClose}
              style={{
                background: 'rgba(255,255,255,0.06)',
                border: '1px solid rgba(255,255,255,0.12)',
                borderRadius: 5,
                padding: '6px 14px',
                color: 'var(--text-primary, #f1f5f9)',
                fontFamily: 'var(--font-mono)',
                fontSize: 11,
                cursor: 'pointer',
              }}
              aria-label={t('ui6PagesApps.close_dialog')}
            >
              {t('ui6PagesApps.close')}
            </button>
          </div>
        </div>
      </div>

      {/* FileViewerModal preview */}
      {previewDoc && (
        <FileViewerModal
          doc={previewDoc}
          onClose={() => setPreviewDoc(null)}
        />
      )}
    </>
  )
}
