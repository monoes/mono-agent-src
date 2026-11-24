import { useEffect, useRef, useState } from 'react'
import { X, ExternalLink, AlertTriangle } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import * as WailsApp from '../wailsjs/go/main/App'
import { useEscapeClose } from '../lib/escapeStack.js'

const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg', 'ico'])
const PDF_EXTS = new Set(['pdf'])
const HTML_EXTS = new Set(['html', 'htm'])
const TEXT_EXTS = new Set([
  'txt', 'md', 'markdown', 'csv', 'json', 'log', 'yaml', 'yml',
  'js', 'jsx', 'ts', 'tsx', 'go', 'py', 'xml', 'ini', 'conf', 'sh', 'toml',
])

// Pure so it's directly unit-testable without rendering the component —
// mirrors Documents.jsx's own formatBytes/isMonomindMissing pattern.
export function fileViewerKind(filename) {
  const ext = (filename || '').split('.').pop()?.toLowerCase() || ''
  if (IMAGE_EXTS.has(ext)) return 'image'
  if (PDF_EXTS.has(ext)) return 'pdf'
  if (HTML_EXTS.has(ext)) return 'html'
  if (TEXT_EXTS.has(ext)) return 'text'
  return null
}

// Pure so it's directly unit-testable without rendering — mirrors
// fileViewerKind above. Deliberately does NOT change what fileViewerKind
// itself returns for .md/.markdown (stays 'text', already relied on by
// existing tests and by the content-fetch dispatch below, which is the
// same for every non-binary kind) -- this just decides, inside the
// existing 'text' render branch, whether to run the content through a
// markdown renderer instead of a bare <pre>.
export function isMarkdownFile(filename) {
  const ext = (filename || '').split('.').pop()?.toLowerCase() || ''
  return ext === 'md' || ext === 'markdown'
}

// Inline-styled overrides for react-markdown's rendered elements, matching
// this modal's existing dark theme (#e2e8f0 body text, #00b4d8 accent —
// see the filename/Open-Externally button above) since everything in this
// file is inline style={{...}}, no CSS module/global stylesheet to extend
// instead. remark-gfm (tables/strikethrough/task lists/autolinks) is
// enabled, but no rehype-raw plugin is added, so raw HTML embedded in a
// document's markdown source is never rendered -- matching the defensive
// posture the html viewer below already takes with its empty iframe
// sandbox, since a "discovered" document's content isn't necessarily
// authored by the user themselves.
export const mdComponents = {
  h1: (p) => <h1 style={{ fontSize: 20, margin: '0.6em 0 0.4em', color: '#f1f5f9', borderBottom: '1px solid #1e3a4f', paddingBottom: 6 }} {...p} />,
  h2: (p) => <h2 style={{ fontSize: 17, margin: '0.6em 0 0.4em', color: '#f1f5f9' }} {...p} />,
  h3: (p) => <h3 style={{ fontSize: 14, margin: '0.6em 0 0.3em', color: '#f1f5f9' }} {...p} />,
  p: (p) => <p style={{ margin: '0.5em 0', lineHeight: 1.6 }} {...p} />,
  a: (p) => <a style={{ color: '#00b4d8' }} target="_blank" rel="noreferrer" {...p} />,
  ul: (p) => <ul style={{ margin: '0.4em 0', paddingLeft: 22 }} {...p} />,
  ol: (p) => <ol style={{ margin: '0.4em 0', paddingLeft: 22 }} {...p} />,
  li: (p) => <li style={{ margin: '0.2em 0' }} {...p} />,
  blockquote: (p) => <blockquote style={{ margin: '0.5em 0', paddingLeft: 12, borderLeft: '3px solid #1e3a4f', color: '#94a3b8' }} {...p} />,
  hr: (p) => <hr style={{ border: 'none', borderTop: '1px solid #1e3a4f', margin: '1em 0' }} {...p} />,
  // react-markdown v9+ dropped the old `inline` prop on `code` (there's no
  // longer a reliable way to distinguish inline from fenced-block code from
  // here alone -- see its readme's own syntax-highlighting example, which
  // only detects a LANGUAGE-TAGGED fence via className, not an untagged
  // one). Sidestepped rather than worked around: `code`'s background here
  // matches `pre`'s exactly, so when a block code's <code> ends up nested
  // inside our styled <pre> below, the two colors don't create a visible
  // seam -- one unified style that looks right standalone (inline) AND
  // nested (block), without needing inline/block detection at all.
  code: (p) => <code style={{ background: '#0d1a26', padding: '1px 5px', borderRadius: 4, fontFamily: 'var(--font-mono)', fontSize: '0.9em' }} {...p} />,
  pre: (p) => <pre style={{ background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 6, padding: 10, overflow: 'auto' }} {...p} />,
  table: (p) => <table style={{ borderCollapse: 'collapse', margin: '0.5em 0', fontSize: '0.95em' }} {...p} />,
  th: (p) => <th style={{ border: '1px solid #1e3a4f', padding: '4px 8px', textAlign: 'left', background: '#0d1a26' }} {...p} />,
  td: (p) => <td style={{ border: '1px solid #1e3a4f', padding: '4px 8px' }} {...p} />,
}

const fmtBytes = (b) => {
  if (!b || b < 1024) return (b || 0) + ' B'
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB'
  return (b / 1024 / 1024).toFixed(1) + ' MB'
}

// In-app preview for a vault document (image, PDF, HTML, or text/source) —
// used for double-click / "View" on Documents.jsx. Files with no supported
// kind never get here: the caller tells the user and hands off to
// WailsApp.OpenPathWithOS instead of opening this modal.
export default function FileViewerModal({ doc, onClose }) {
  const overlayRef = useRef(null)
  const [content, setContent] = useState(null) // data URL (image/pdf) or raw text (html/text)
  const [error, setError] = useState(null)
  const kind = fileViewerKind(doc?.filename)

  useEscapeClose(onClose, !!doc)

  useEffect(() => {
    if (!doc || !kind) return
    setContent(null)
    setError(null)
    const load = kind === 'image' || kind === 'pdf'
      ? WailsApp.GetProfileDocumentData(doc.id)
      : WailsApp.GetProfileDocumentText(doc.id)
    load.then(setContent).catch(e => setError(String(e)))
  }, [doc?.id, kind])

  const handleOpenWithOS = () => {
    WailsApp.OpenPathWithOS(doc.path).catch(e => setError(String(e)))
  }

  if (!doc) return null

  return (
    <div
      ref={overlayRef}
      onClick={(e) => { if (e.target === overlayRef.current) onClose() }}
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.75)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div role="dialog" aria-modal="true" aria-label="File preview" style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 12,
        padding: 20, width: 760, maxWidth: '92vw', maxHeight: '88vh',
        display: 'flex', flexDirection: 'column', gap: 12,
        boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, color: '#00b4d8', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {doc.filename}
            </div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>{fmtBytes(doc.size_bytes)}</div>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
            <button
              onClick={handleOpenWithOS}
              title="Open with system default application"
              style={{
                background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
                borderRadius: 6, padding: '5px 10px', color: '#00b4d8',
                fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
                display: 'flex', alignItems: 'center', gap: 5,
              }}
            >
              <ExternalLink size={12} /> Open Externally
            </button>
            <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 2 }}>
              <X size={16} />
            </button>
          </div>
        </div>

        <div style={{
          flex: 1, background: '#060b11', borderRadius: 8, overflow: 'auto',
          border: '1px solid #1e3a4f', minHeight: 200,
          display: 'flex', alignItems: error ? 'center' : 'stretch', justifyContent: error ? 'center' : 'stretch',
        }}>
          {error ? (
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8, color: '#fca5a5', padding: 24, textAlign: 'center' }}>
              <AlertTriangle size={24} />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}>{error}</span>
            </div>
          ) : content === null ? (
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', width: '100%', color: '#334155', fontFamily: 'var(--font-mono)', fontSize: 11 }}>
              Loading…
            </div>
          ) : kind === 'image' ? (
            <img src={content} alt={doc.filename} style={{ maxWidth: '100%', maxHeight: '70vh', objectFit: 'contain', margin: 'auto', display: 'block' }} />
          ) : kind === 'pdf' ? (
            <embed src={content} type="application/pdf" style={{ width: '100%', height: '70vh', border: 'none' }} />
          ) : kind === 'html' ? (
            <iframe
              srcDoc={content}
              sandbox=""
              title={doc.filename}
              style={{ width: '100%', height: '70vh', border: 'none', background: '#fff' }}
            />
          ) : isMarkdownFile(doc.filename) ? (
            <div style={{
              width: '100%', maxHeight: '70vh', overflow: 'auto', padding: '14px 18px',
              fontFamily: 'var(--font-sans, sans-serif)', fontSize: 13, color: '#e2e8f0',
            }}>
              <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>{content}</ReactMarkdown>
            </div>
          ) : (
            <pre style={{
              margin: 0, padding: 14, width: '100%', maxHeight: '70vh',
              overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0',
            }}>{content}</pre>
          )}
        </div>
      </div>
    </div>
  )
}
