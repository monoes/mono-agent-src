import { createContext, useContext, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { FileText, X } from 'lucide-react'
import { api } from '../../services/api.js'
import { isImage, formatBytes, imagePathsInResult, baseName } from './chatAttachments.js'

// The images and files of a coder chat (C4): the composer's attachment
// chips, the thumbnails under a sent message, and the images a tool result
// names (a `browser screenshot`), read from the chat's folder through
// ReadWorkspaceImage. Click an image to see it large.

const mono = 'var(--font-mono)'

// WorkspaceImageContext is the coder conversation whose folder images are
// read from; "" outside a coder chat, where no tool card shows images.
export const WorkspaceImageContext = createContext('')

// Data URLs already read, by conversation and path, so a re-render or a
// re-expanded bubble does not read the file again.
const cache = new Map()
const CACHE_SIZE = 60

function loadWorkspaceImage(conversationId, path) {
  const key = `${conversationId}\n${path}`
  let p = cache.get(key)
  if (!p) {
    p = Promise.resolve().then(() => api.readWorkspaceImage(conversationId, path))
    p.catch(() => cache.delete(key))
    cache.set(key, p)
    if (cache.size > CACHE_SIZE) cache.delete(cache.keys().next().value)
  }
  return p
}

export function ImageLightbox({ src, alt, onClose }) {
  useEffect(() => {
    const onKey = e => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return createPortal(
    <div role="dialog" aria-modal="true" aria-label={alt} data-testid="image-lightbox" onClick={onClose} style={{
      position: 'fixed', inset: 0, zIndex: 10000, background: 'rgba(2,5,9,0.88)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24, cursor: 'zoom-out',
    }}>
      <img src={src} alt={alt} style={{ maxWidth: '100%', maxHeight: '100%', objectFit: 'contain', borderRadius: 6, boxShadow: '0 8px 40px rgba(0,0,0,0.6)' }} />
      <button type="button" aria-label="Close image" onClick={onClose} style={{
        position: 'absolute', top: 12, right: 12, background: 'rgba(0,0,0,0.5)', border: '1px solid rgba(255,255,255,0.2)',
        borderRadius: 6, color: '#e2e8f0', cursor: 'pointer', padding: 4, display: 'flex',
      }}>
        <X size={14} />
      </button>
    </div>,
    document.body,
  )
}

const thumbButton = {
  padding: 0, border: '1px solid rgba(0,180,216,0.2)', borderRadius: 6, background: '#020509',
  cursor: 'zoom-in', overflow: 'hidden', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
}

// Thumb is one clickable image that opens large.
function Thumb({ src, alt, maxHeight, maxWidth = 220, testId }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" data-testid={testId} title={alt} aria-label={`Open ${alt}`} onClick={() => setOpen(true)} style={thumbButton}>
        <img src={src} alt={alt} style={{ display: 'block', maxHeight, maxWidth, objectFit: 'contain' }} />
      </button>
      {open && <ImageLightbox src={src} alt={alt} onClose={() => setOpen(false)} />}
    </>
  )
}

// WorkspaceImage is an image in the chat's folder, read once it scrolls
// into view. One that cannot be read (gone, outside the folder, not an
// image) is left out.
export function WorkspaceImage({ conversationId, path, maxHeight = 160 }) {
  const ref = useRef(null)
  const [visible, setVisible] = useState(() => typeof IntersectionObserver === 'undefined')
  const [src, setSrc] = useState('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (visible || !ref.current) return undefined
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) { setVisible(true); io.disconnect() }
    }, { rootMargin: '200px' })
    io.observe(ref.current)
    return () => io.disconnect()
  }, [visible])

  useEffect(() => {
    if (!visible || !conversationId || !path) return undefined
    let live = true
    loadWorkspaceImage(conversationId, path).then(
      url => { if (live) setSrc(url) },
      () => { if (live) setFailed(true) },
    )
    return () => { live = false }
  }, [visible, conversationId, path])

  if (failed) return null
  if (!src) {
    return <span ref={ref} data-testid="workspace-image-loading" style={{ display: 'inline-block', width: 64, height: Math.min(maxHeight, 48), borderRadius: 6, background: 'rgba(0,180,216,0.06)' }} />
  }
  return <Thumb src={src} alt={baseName(path)} maxHeight={maxHeight} testId="workspace-image" />
}

// ToolResultImages shows the images a finished tool call's result names
// (e.g. `monoagentcli browser screenshot --json` → {"path": …}), when the
// card is in a coder chat.
export function ToolResultImages({ call }) {
  const conversationId = useContext(WorkspaceImageContext)
  if (!conversationId || call.status !== 'completed' || call.ok === false) return null
  const paths = imagePathsInResult(call.result)
  if (paths.length === 0) return null
  return (
    <div data-testid="tool-result-images" style={{ display: 'flex', flexWrap: 'wrap', gap: 6, padding: '0 10px 8px' }}>
      {paths.map(p => <WorkspaceImage key={p} conversationId={conversationId} path={p} />)}
    </div>
  )
}

function FileChip({ name, size, children }) {
  return (
    <span title={name} style={{
      display: 'inline-flex', alignItems: 'center', gap: 5, maxWidth: 200, padding: '4px 8px',
      border: '1px solid rgba(0,180,216,0.2)', borderRadius: 6, background: '#020509',
      fontFamily: mono, fontSize: 10, color: '#cbd5e1',
    }}>
      <FileText size={11} color="#00b4d8" style={{ flexShrink: 0 }} />
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</span>
      {size != null && <span style={{ color: 'var(--text-muted)', flexShrink: 0 }}>{formatBytes(size)}</span>}
      {children}
    </span>
  )
}

// AttachmentChips are the composer's attachments before sending: image
// thumbnails and file chips, each with a remove button.
export function AttachmentChips({ attachments, onRemove }) {
  if (!attachments?.length) return null
  const card = {
    position: 'relative', display: 'inline-flex', flexDirection: 'column', gap: 4, padding: 5,
    border: '1px solid rgba(0,180,216,0.25)', borderRadius: 8, background: 'rgba(0,180,216,0.04)',
  }
  const caption = { display: 'flex', alignItems: 'baseline', gap: 5, fontFamily: mono, fontSize: 9, color: '#cbd5e1' }
  return (
    <div data-testid="attachment-chips" style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
      {attachments.map(a => {
        const remove = (
          <button type="button" aria-label={`Remove ${a.name}`} title={`Remove ${a.name}`} onClick={() => onRemove(a.id)} style={{
            position: 'absolute', top: -6, right: -6, width: 18, height: 18, borderRadius: '50%',
            background: '#0b1220', border: '1px solid rgba(0,180,216,0.4)', color: '#e2e8f0', cursor: 'pointer',
            padding: 0, display: 'flex', alignItems: 'center', justifyContent: 'center',
          }}>
            <X size={10} />
          </button>
        )
        const label = (
          <span style={caption}>
            <span title={a.name} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>{a.name}</span>
            {a.size != null && <span style={{ color: 'var(--text-muted)', flexShrink: 0 }}>{formatBytes(a.size)}</span>}
          </span>
        )
        if (isImage(a.mime) && a.dataUrl) {
          return (
            <span key={a.id} data-testid="attachment-chip" style={{ ...card, width: 120 }}>
              <Thumb src={a.dataUrl} alt={a.name} maxHeight={84} maxWidth={108} />
              {label}
              {remove}
            </span>
          )
        }
        return (
          <span key={a.id} data-testid="attachment-chip" style={{ ...card, flexDirection: 'row', alignItems: 'center', gap: 8, padding: '7px 10px', maxWidth: 240 }}>
            <span style={{ width: 30, height: 30, borderRadius: 6, background: 'rgba(0,180,216,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
              <FileText size={15} color="#00b4d8" />
            </span>
            <span style={{ ...caption, flexDirection: 'column', alignItems: 'flex-start', gap: 1, minWidth: 0, fontSize: 10 }}>
              <span title={a.name} style={{ maxWidth: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.name}</span>
              {a.size != null && <span style={{ color: 'var(--text-muted)', fontSize: 9 }}>{formatBytes(a.size)}</span>}
            </span>
            {remove}
          </span>
        )
      })}
    </div>
  )
}

// MessageAttachments are the thumbnails under a sent message: the images in
// memory for a message just sent, or read from the chat's folder (the
// copies `turn.started` lists) for one loaded from history.
export function MessageAttachments({ attachments, conversationId }) {
  if (!attachments?.length) return null
  return (
    <div data-testid="message-attachments" style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'flex-end', gap: 6, margin: '-4px 0 8px' }}>
      {attachments.map((a, i) => {
        if (isImage(a.mime) && a.dataUrl) return <Thumb key={i} src={a.dataUrl} alt={a.name} maxHeight={72} testId="message-image" />
        if (isImage(a.mime) && a.path && conversationId) return <WorkspaceImage key={i} conversationId={conversationId} path={a.path} maxHeight={72} />
        return <FileChip key={i} name={a.name} size={a.size} />
      })}
    </div>
  )
}
