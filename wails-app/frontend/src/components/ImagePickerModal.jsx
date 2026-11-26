import { useState, useEffect, useRef } from 'react'
import { X, Search } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { isVaultImage } from '../lib/vaultItems.js'
import { useEscapeClose } from '../lib/escapeStack.js'
import { useTranslation } from 'react-i18next'
import i18nOr from '../lib/i18nOr.js'

function VaultThumb({ id }) {
  const [src, setSrc] = useState(null)
  useEffect(() => {
    try {
      const p = WailsApp.GetVaultImageData(id)
      if (p && typeof p.then === 'function') {
        p.then(setSrc).catch(() => {})
      }
    } catch (_) {}
  }, [id])
  if (!src) return null
  return <img src={src} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
}

export default function ImagePickerModal({ onSelect, onClose }) {
  useTranslation() // render again on a language change
  const [images, setImages] = useState([])
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(null)
  const searchRef = useRef(null)

  useEffect(() => {
    // The vault also holds files (PDFs, text the extension saved): only
    // images can be picked here.
    WailsApp.GetVaultImages(100).then(items => setImages((items || []).filter(isVaultImage))).catch(() => {})
    const focusTimer = setTimeout(() => searchRef.current?.focus(), 50)
    return () => clearTimeout(focusTimer)
  }, [])

  useEscapeClose(onClose)

  const filtered = query
    ? images.filter(img =>
        img.id.includes(query) ||
        (img.label || '').toLowerCase().includes(query.toLowerCase()) ||
        img.filename.toLowerCase().includes(query.toLowerCase())
      )
    : images

  const fmtBytes = (b) => {
    if (!b || b < 1024) return (b || 0) + ' B'
    if (b < 1024 * 1024) return (b / 1024).toFixed(0) + ' KB'
    return (b / 1024 / 1024).toFixed(1) + ' MB'
  }

  return (
    <div
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
      style={{
        position: 'fixed', inset: 0, zIndex: 1100,
        background: 'rgba(0,0,0,0.75)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div role="dialog" aria-modal="true" aria-label={i18nOr('ui6PagesImages.pick')} style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 10,
        padding: 16, width: 360, maxWidth: '90vw',
        display: 'flex', flexDirection: 'column', gap: 10,
        boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: '#e2e8f0' }}>{i18nOr('ui6PagesImages.pick')}</span>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569' }}>
            <X size={14} />
          </button>
        </div>

        <div style={{ position: 'relative' }}>
          <Search size={12} style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: '#475569' }} />
          <input
            ref={searchRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder={i18nOr('ui6PagesImages.search')}
            style={{
              width: '100%', background: '#060b11', border: '1px solid #1e3a4f',
              borderRadius: 5, padding: '5px 8px 5px 26px', color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 11, boxSizing: 'border-box',
            }}
          />
        </div>

        <div style={{ maxHeight: 260, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 3 }}>
          {filtered.length === 0 && (
            <div style={{ color: '#475569', fontFamily: 'var(--font-mono)', fontSize: 11, padding: '12px 0', textAlign: 'center' }}>
              {i18nOr('ui6PagesImages.none')}
            </div>
          )}
          {filtered.map(img => (
            <div
              key={img.id}
              onClick={() => setSelected(img.id === selected ? null : img.id)}
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                background: selected === img.id ? '#1e3a2f' : '#111827',
                border: `1px solid ${selected === img.id ? 'rgba(16,185,129,0.3)' : '#1e3a4f'}`,
                borderRadius: 5, padding: '6px 8px', cursor: 'pointer',
              }}
            >
              <div style={{ width: 32, height: 32, borderRadius: 3, overflow: 'hidden', flexShrink: 0, background: '#060b11' }}>
                <VaultThumb id={img.id} />
              </div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: selected === img.id ? '#10b981' : '#00b4d8' }}>{img.id}</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {img.label || img.filename} · {fmtBytes(img.size_bytes)}
                </div>
              </div>
            </div>
          ))}
        </div>

        <div style={{ display: 'flex', gap: 8 }}>
          <button
            onClick={() => { if (selected) { onSelect('@' + selected); onClose() } }}
            disabled={!selected}
            style={{
              flex: 1, background: selected ? 'rgba(0,180,216,0.1)' : '#060b11',
              border: `1px solid ${selected ? 'rgba(0,180,216,0.4)' : '#1e3a4f'}`,
              borderRadius: 6, padding: '7px 12px',
              color: selected ? '#00b4d8' : '#334155',
              fontFamily: 'var(--font-mono)', fontSize: 11, cursor: selected ? 'pointer' : 'not-allowed',
            }}
          >
            {i18nOr('ui6PagesImages.select')}
          </button>
          <button
            onClick={onClose}
            style={{
              background: '#060b11', border: '1px solid #1e3a4f',
              borderRadius: 6, padding: '7px 14px', color: '#475569',
              fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
            }}
          >
            {i18nOr('ui6PagesImages.cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}
