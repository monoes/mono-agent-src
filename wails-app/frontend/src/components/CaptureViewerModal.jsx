import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n.js'
import { X, ExternalLink, AlertTriangle } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import * as WailsApp from '../wailsjs/go/main/App'
import { mdComponents } from './FileViewerModal.jsx'
import { useEscapeClose } from '../lib/escapeStack.js'

// A browser capture previewed from its parts: the AI summary (when the
// capture asked for one), the page's readable text, a video's transcript,
// and the screenshot. Its primary file is often page.mhtml, which nothing
// in-app can show and which the OS tends to hand to a text editor.

// Below this many words the "text" is a leftover (a search page's one
// snippet, an app's empty shell), and the screenshot says more.
const MIN_READABLE_WORDS = 60

// Pure, for tests: which tab a capture opens on. A written summary is the
// reason the capture was saved that way, so it wins.
export function initialCaptureTab(view) {
  if (!view) return 'text'
  if (view.summary) return 'summary'
  const words = (view.readable || '').split(/\s+/).filter(Boolean).length
  if (view.screenshot && words < MIN_READABLE_WORDS) return 'screenshot'
  return view.readable ? 'text' : 'screenshot'
}

// Pure, for tests: the one line that says where a summary is. The
// Documents list's badge uses it too.
export function summaryStatusText(st) {
  if (!st || !st.status) return ''
  switch (st.status) {
    case 'done': return st.runtime ? i18n.t('ui6PagesViewers.summary_by', { runtime: st.runtime }) : i18n.t('ui6PagesViewers.summary')
    case 'pending': return st.runtime ? i18n.t('ui6PagesViewers.summary_queued_runtime', { runtime: st.runtime }) : i18n.t('ui6PagesViewers.summary_queued')
    case 'running': return st.runtime ? i18n.t('ui6PagesViewers.summary_writing_by', { runtime: st.runtime }) : i18n.t('ui6PagesViewers.summary_writing')
    case 'stalled': return i18n.t('ui6PagesViewers.summary_stalled')
    case 'error': return st.error ? i18n.t('ui6PagesViewers.summary_failed_error', { error: st.error }) : i18n.t('ui6PagesViewers.summary_failed')
    default: return st.status
  }
}

const tabStyle = (active) => ({
  background: active ? 'rgba(0,180,216,0.15)' : 'none',
  border: '1px solid ' + (active ? 'rgba(0,180,216,0.45)' : '#1e3a4f'),
  borderRadius: 6, padding: '4px 10px', cursor: 'pointer',
  color: active ? '#00b4d8' : '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11,
})

export default function CaptureViewerModal({ doc, onClose }) {
  const { t } = useTranslation()
  const overlayRef = useRef(null)
  const [view, setView] = useState(null)
  const [error, setError] = useState(null)
  const [tab, setTab] = useState('text')

  useEscapeClose(onClose, !!doc)

  useEffect(() => {
    if (!doc) return
    setView(null)
    setError(null)
    WailsApp.GetCaptureView(doc.id)
      .then(v => { setView(v); setTab(initialCaptureTab(v)) })
      .catch(e => setError(String(e)))
  }, [doc?.id])

  if (!doc) return null
  const url = view?.url || doc.url

  return (
    <div
      ref={overlayRef}
      onClick={(e) => { if (e.target === overlayRef.current) onClose() }}
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,0.75)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}
    >
      <div role="dialog" aria-modal="true" aria-label={t('ui6PagesViewers.capture_preview')} style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 12,
        padding: 20, width: 860, maxWidth: '92vw', maxHeight: '88vh',
        display: 'flex', flexDirection: 'column', gap: 12, boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 14, color: '#e2e8f0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {view?.title || doc.filename}
            </div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={url}>
              {t('ui6PagesViewers.saved_from_extension')}{doc.created_at ? ` · ${doc.created_at}` : ''}{url ? ` · ${url}` : ''}
            </div>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
            {url && (
              <button onClick={() => WailsApp.OpenURL(url)} title={t('ui6PagesViewers.open_original_title')} style={{ ...tabStyle(false), color: '#00b4d8', display: 'flex', alignItems: 'center', gap: 5 }}>
                <ExternalLink size={12} /> {t('ui6PagesViewers.open_original')}
              </button>
            )}
            <button onClick={onClose} aria-label={t('ui6PagesViewers.close')} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 2 }}>
              <X size={16} />
            </button>
          </div>
        </div>

        {view && (
          <div role="tablist" style={{ display: 'flex', gap: 6 }}>
            {(view.summary || view.summary_status) && (
              <button role="tab" aria-selected={tab === 'summary'} onClick={() => setTab('summary')} style={tabStyle(tab === 'summary')}>
                {view.summary ? t('ui6PagesViewers.summary') : t('ui6PagesViewers.tab_summary_not_ready')}
              </button>
            )}
            <button role="tab" aria-selected={tab === 'text'} disabled={!view.readable} onClick={() => setTab('text')} style={{ ...tabStyle(tab === 'text'), opacity: view.readable ? 1 : 0.4 }}>
              {view.readable ? t('ui6PagesViewers.tab_text') : t('ui6PagesViewers.tab_text_none')}
            </button>
            {view.transcript && (
              <button role="tab" aria-selected={tab === 'transcript'} onClick={() => setTab('transcript')} style={tabStyle(tab === 'transcript')}>
                {t('ui6PagesViewers.tab_transcript')}
              </button>
            )}
            <button role="tab" aria-selected={tab === 'screenshot'} disabled={!view.screenshot} onClick={() => setTab('screenshot')} style={{ ...tabStyle(tab === 'screenshot'), opacity: view.screenshot ? 1 : 0.4 }}>
              {t('ui6PagesViewers.tab_screenshot')}
            </button>
          </div>
        )}

        <div style={{
          flex: 1, background: '#060b11', borderRadius: 8, overflow: 'auto', border: '1px solid #1e3a4f', minHeight: 200,
          display: 'flex', alignItems: error ? 'center' : 'stretch', justifyContent: error ? 'center' : 'stretch',
        }}>
          {error ? (
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8, color: '#fca5a5', padding: 24, textAlign: 'center' }}>
              <AlertTriangle size={24} />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}>{error}</span>
            </div>
          ) : view === null ? (
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', width: '100%', color: '#334155', fontFamily: 'var(--font-mono)', fontSize: 11 }}>{t('ui6PagesViewers.loading')}</div>
          ) : tab === 'summary' ? (
            <div style={{ width: '100%', maxHeight: '70vh', overflow: 'auto', padding: '14px 18px', fontSize: 13, color: '#e2e8f0' }}>
              {view.summary_status && view.summary_status.status !== 'done' && (
                <div role="status" style={{ fontFamily: 'var(--font-mono)', fontSize: 11, marginBottom: 10, color: ['error', 'stalled'].includes(view.summary_status.status) ? '#fca5a5' : '#94a3b8' }}>
                  {summaryStatusText(view.summary_status)}
                </div>
              )}
              {view.summary && <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>{view.summary}</ReactMarkdown>}
            </div>
          ) : tab === 'transcript' && view.transcript ? (
            <div style={{ width: '100%', maxHeight: '70vh', overflow: 'auto', padding: '14px 18px', fontSize: 13, color: '#e2e8f0' }}>
              <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>{view.transcript}</ReactMarkdown>
            </div>
          ) : tab === 'screenshot' && view.screenshot ? (
            <img src={view.screenshot} alt={t('ui6PagesViewers.screenshot_of', { name: view.title || doc.filename })} style={{ width: '100%', height: 'auto', display: 'block', alignSelf: 'flex-start' }} />
          ) : view.readable ? (
            <div style={{ width: '100%', maxHeight: '70vh', overflow: 'auto', padding: '14px 18px', fontSize: 13, color: '#e2e8f0' }}>
              <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>{view.readable}</ReactMarkdown>
            </div>
          ) : (
            <div style={{ margin: 'auto', color: '#475569', fontFamily: 'var(--font-mono)', fontSize: 11 }}>{t('ui6PagesViewers.capture_empty')}</div>
          )}
        </div>
      </div>
    </div>
  )
}
