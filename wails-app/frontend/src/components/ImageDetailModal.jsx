import { useEffect, useRef, useState } from 'react'
import { X, Copy, Trash2, Edit3, Check, AlertTriangle, Wand2, Download, FileText } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import ImageEditorFullscreen from './ImageEditorFullscreen'
import { isVaultImage, vaultFileKind, vaultKind } from '../lib/vaultItems.js'

export default function ImageDetailModal({ image, onClose, onDelete, onRename, onSaved }) {
  const overlayRef = useRef(null)
  const [dataUrl, setDataUrl] = useState(null)
  const [imgError, setImgError] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const [renameValue, setRenameValue] = useState('')
  const [copied, setCopied] = useState(false)
  const [editing, setEditing] = useState(false)

  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  useEffect(() => {
    if (!image) return
    setDataUrl(null)
    setImgError(false)
    setConfirmDelete(false)
    setRenaming(false)
    // Only an image has a preview to fetch: audio and video play from the
    // app's asset route, and a PDF or text file has nothing to show.
    if (!isVaultImage(image)) return
    try {
      const p = WailsApp.GetVaultImageData(image.id)
      if (p && typeof p.then === 'function') {
        p.then(url => setDataUrl(url)).catch(() => setImgError(true))
      }
    } catch (_) { setImgError(true) }
  }, [image?.id])

  if (!image) return null
  const isImage = isVaultImage(image)
  const kind = vaultKind(image)

  const handleDelete = async () => {
    if (!confirmDelete) { setConfirmDelete(true); return }
    try {
      await WailsApp.DeleteVaultImage(image.id)
      onDelete(image.id)
      onClose()
    } catch (e) {
      setConfirmDelete(false)
    }
  }

  const handleRename = async () => {
    if (!renaming) {
      setRenameValue(image.label || '')
      setRenaming(true)
      return
    }
    try {
      await WailsApp.UpdateVaultImageLabel(image.id, renameValue)
      onRename && onRename({ ...image, label: renameValue })
      setRenaming(false)
      onClose()
    } catch (_) { setRenaming(false) }
  }

  const handleSave = async () => {
    const ext = image.filename?.split('.').pop() || 'png'
    const name = (image.label || image.filename || image.id).replace(/[^a-z0-9_\-. ]/gi, '_')
    const suggestedName = name.endsWith('.' + ext) ? name : name + '.' + ext
    await WailsApp.SaveVaultImageToFile(image.id, suggestedName)
  }

  const copyRef = () => {
    const text = '@' + image.id
    try {
      navigator.clipboard.writeText(text).catch(() => fallbackCopy(text))
    } catch (_) { fallbackCopy(text) }
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  const fallbackCopy = (text) => {
    const el = document.createElement('textarea')
    el.value = text
    el.style.position = 'fixed'
    el.style.opacity = '0'
    document.body.appendChild(el)
    el.select()
    document.execCommand('copy')
    document.body.removeChild(el)
  }

  const fmtBytes = (b) => {
    if (!b || b < 1024) return (b || 0) + ' B'
    if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB'
    return (b / 1024 / 1024).toFixed(1) + ' MB'
  }

  const fmtDate = (s) => {
    if (!s) return '—'
    const iso = s.includes('T') ? s : s.replace(' ', 'T')
    const d = new Date(iso.endsWith('Z') ? iso : iso + 'Z')
    return isNaN(d) ? s : d.toLocaleDateString()
  }

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
      <div role="dialog" aria-modal="true" aria-label="Image detail" style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 12,
        padding: 20, width: 420, maxWidth: '90vw',
        display: 'flex', flexDirection: 'column', gap: 14,
        boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
      }}>
        {/* Header */}
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 13, color: '#00b4d8' }}>
            {image.id}{image.label ? ` · ${image.label}` : ''}
          </span>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 2 }}>
            <X size={16} />
          </button>
        </div>

        {/* Preview: image, audio or video player, or a file card */}
        <div style={{
          background: '#060b11', borderRadius: 8, overflow: 'hidden',
          border: '1px solid #1e3a4f', minHeight: 120, maxHeight: 280,
          display: 'flex', alignItems: 'center', justifyContent: 'center',
        }}>
          {kind === 'video' ? (
            <video
              key={image.id}
              src={image.url || `/vault-image/${image.id}`}
              controls
              preload="metadata"
              aria-label={`video ${image.label || image.filename}`}
              style={{ maxWidth: '100%', maxHeight: 280, display: 'block' }}
            />
          ) : kind === 'audio' ? (
            <audio
              key={image.id}
              src={image.url || `/vault-image/${image.id}`}
              controls
              preload="metadata"
              aria-label={`audio ${image.label || image.filename}`}
              style={{ width: '100%', margin: 20 }}
            />
          ) : !isImage ? (
            <div
              role="img"
              aria-label={`file ${image.label || image.filename}`}
              style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6, color: '#64748b', padding: 20 }}
            >
              <FileText size={28} />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0', wordBreak: 'break-all', textAlign: 'center' }}>{image.label || image.filename}</span>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10 }}>{vaultFileKind(image)}</span>
            </div>
          ) : imgError ? (
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6, color: '#334155', padding: 20 }}>
              <span style={{ fontSize: 28 }}>🖼</span>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10 }}>Image unavailable</span>
            </div>
          ) : dataUrl ? (
            <img
              src={dataUrl}
              alt={image.label || image.id}
              style={{ maxWidth: '100%', maxHeight: 280, objectFit: 'contain', display: 'block' }}
              onError={() => setImgError(true)}
            />
          ) : (
            <div style={{ color: '#334155', fontFamily: 'var(--font-mono)', fontSize: 10, padding: 20 }}>Loading…</div>
          )}
        </div>

        {/* Metadata */}
        <div style={{ display: 'flex', gap: 16, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', flexWrap: 'wrap' }}>
          <span style={{ color: '#a78bfa' }}>{image.source}</span>
          <span>{fmtBytes(image.size_bytes)}</span>
          <span>{fmtDate(image.created_at)}</span>
          {image.workflow_id && <span style={{ color: '#64748b' }}>{image.workflow_id}</span>}
        </div>

        {/* Rename input */}
        {renaming && (
          <div style={{ display: 'flex', gap: 6 }}>
            <input
              autoFocus
              value={renameValue}
              onChange={e => setRenameValue(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleRename(); if (e.key === 'Escape') setRenaming(false) }}
              placeholder="Label…"
              style={{
                flex: 1, background: '#060b11', border: '1px solid rgba(0,180,216,0.4)',
                borderRadius: 5, padding: '6px 8px', color: '#e2e8f0',
                fontFamily: 'var(--font-mono)', fontSize: 11,
              }}
            />
            <button
              onClick={handleRename}
              style={{
                background: 'rgba(0,180,216,0.15)', border: '1px solid rgba(0,180,216,0.4)',
                borderRadius: 5, padding: '6px 10px', color: '#00b4d8', cursor: 'pointer',
                fontFamily: 'var(--font-mono)', fontSize: 11,
              }}
            >Save</button>
            <button
              onClick={() => setRenaming(false)}
              style={{
                background: 'none', border: '1px solid #1e3a4f',
                borderRadius: 5, padding: '6px 10px', color: '#475569', cursor: 'pointer',
                fontFamily: 'var(--font-mono)', fontSize: 11,
              }}
            >Cancel</button>
          </div>
        )}

        {/* Actions */}
        {!renaming && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {/* Adjust full-screen button: images only */}
            {isImage && <button
              onClick={() => setEditing(true)}
              style={{
                width: '100%', background: 'rgba(0,180,216,0.1)',
                border: '1px solid rgba(0,180,216,0.35)', borderRadius: 6,
                padding: '8px 12px', color: '#00b4d8',
                fontFamily: 'var(--font-mono)', fontSize: 12, cursor: 'pointer',
                display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 7,
              }}
            >
              <Wand2 size={13} /> Adjust Image
            </button>}

            {/* Save to disk */}
            <button
              onClick={handleSave}
              style={{
                width: '100%', background: 'rgba(16,185,129,0.08)',
                border: '1px solid rgba(16,185,129,0.3)', borderRadius: 6,
                padding: '8px 12px', color: '#34d399',
                fontFamily: 'var(--font-mono)', fontSize: 12, cursor: 'pointer',
                display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 7,
              }}
            >
              <Download size={13} /> {isImage ? 'Save Image' : 'Save File'}
            </button>

            <div style={{ display: 'flex', gap: 8 }}>
            <button
              onClick={copyRef}
              style={{
                flex: 1, background: copied ? 'rgba(16,185,129,0.1)' : '#0a1829',
                border: `1px solid ${copied ? 'rgba(16,185,129,0.4)' : 'rgba(0,180,216,0.3)'}`,
                borderRadius: 6, padding: '7px 12px',
                color: copied ? '#34d399' : '#00b4d8',
                fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
                display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 5,
                transition: 'all 0.15s',
              }}
            >
              {copied ? <><Check size={12} /> Copied!</> : <><Copy size={12} /> Copy @{image.id}</>}
            </button>

            <button
              onClick={handleRename}
              style={{
                background: '#0a1829', border: '1px solid #1e3a4f',
                borderRadius: 6, padding: '7px 10px', color: '#475569',
                cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4,
                fontFamily: 'var(--font-mono)', fontSize: 11,
              }}
            >
              <Edit3 size={12} /> Rename
            </button>

            <button
              onClick={handleDelete}
              style={{
                background: confirmDelete ? 'rgba(239,68,68,0.2)' : 'rgba(239,68,68,0.08)',
                border: '1px solid rgba(239,68,68,0.4)',
                borderRadius: 6, padding: '7px 10px',
                color: confirmDelete ? '#fca5a5' : '#ef4444',
                cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4,
                fontFamily: 'var(--font-mono)', fontSize: 11, transition: 'all 0.15s',
              }}
            >
              {confirmDelete ? <><AlertTriangle size={12} /> Confirm</> : <><Trash2 size={12} /> Delete</>}
            </button>
          </div>
          </div>
        )}

        {/* Full-screen editor */}
        {editing && (
          <ImageEditorFullscreen
            image={image}
            onClose={() => setEditing(false)}
            onSaved={() => { onSaved?.(); setEditing(false) }}
          />
        )}
      </div>
    </div>
  )
}
