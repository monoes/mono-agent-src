import { useState, useEffect, useCallback, useRef } from 'react'
import { Trash2, Plus, Search, Image as ImageIcon, FileText, Music, Film } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { isVaultImage, vaultFileKind, vaultKind } from '../lib/vaultItems.js'
import ImageDetailModal from '../components/ImageDetailModal'
import RefreshButton from '../components/RefreshButton.jsx'
import { limiter } from '../lib/limiter.js'

// Each thumbnail is one `monoagentcli media data` call, so they load when
// scrolled into view and at most 4 at a time — a vault of hundreds of images
// must not start hundreds of processes at once.
const loadThumb = limiter(4)
import { onImagesChanged } from '../services/api.js'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'

function VaultThumb({ id }) {
  const [src, setSrc] = useState(null)
  const [visible, setVisible] = useState(typeof IntersectionObserver === 'undefined')
  const ref = useRef(null)
  useEffect(() => {
    if (visible || !ref.current) return undefined
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) { setVisible(true); io.disconnect() }
    }, { rootMargin: '200px' })
    io.observe(ref.current)
    return () => io.disconnect()
  }, [visible])
  useEffect(() => {
    if (!visible) return undefined
    let cancelled = false
    loadThumb(() => (cancelled ? null : WailsApp.GetVaultImageData(id)))
      .then(url => { if (!cancelled && url) setSrc(url) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [id, visible])
  if (!src) return <div ref={ref} style={{ width: '100%', height: '100%' }} />
  return <img src={src} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover', display: 'block' }} />
}

// FileThumb stands in for a thumbnail on a vault item that is not an image
// (audio, video, or a PDF or text file the extension saved): an icon and its
// kind, no <img>.
function FileThumb({ item }) {
  const kind = vaultKind(item)
  const Icon = kind === 'audio' ? Music : kind === 'video' ? Film : FileText
  return (
    <div
      role="img"
      aria-label={`${kind || 'file'} ${item.label || item.filename}`}
      title={item.label || item.filename}
      style={{
        width: '100%', height: '100%', display: 'flex', flexDirection: 'column',
        alignItems: 'center', justifyContent: 'center', color: '#64748b', gap: 1,
      }}
    >
      <Icon size={14} />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 7 }}>{vaultFileKind(item)}</span>
    </div>
  )
}

const fmtBytes = (b) => {
  if (!b) return '0 B'
  if (b < 1024) return b + ' B'
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB'
  return (b / 1024 / 1024).toFixed(1) + ' MB'
}

const fmtDate = (s) => {
  if (!s) return '—'
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z')
  if (isNaN(d)) return s
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

const SOURCE_COLORS = {
  gemini:      { bg: 'rgba(124,58,237,0.15)', border: 'rgba(124,58,237,0.3)', color: '#a78bfa' },
  upload:      { bg: 'rgba(16,185,129,0.1)',  border: 'rgba(16,185,129,0.25)', color: '#34d399' },
  huggingface: { bg: 'rgba(0,180,216,0.1)',   border: 'rgba(0,180,216,0.25)',  color: '#00b4d8' },
  openrouter:  { bg: 'rgba(245,158,11,0.15)', border: 'rgba(245,158,11,0.3)',  color: '#fbbf24' },
  workflow:    { bg: 'rgba(59,130,246,0.15)', border: 'rgba(59,130,246,0.3)',  color: '#60a5fa' },
  chat:        { bg: 'rgba(236,72,153,0.15)', border: 'rgba(236,72,153,0.3)',  color: '#f472b6' },
  discovered:  { bg: 'rgba(99,102,241,0.15)', border: 'rgba(99,102,241,0.3)',  color: '#818cf8' },
}
const sourceBadge = (source) => {
  const s = SOURCE_COLORS[source] || { bg: '#1a2332', border: '#334', color: '#64748b' }
  return (
    <span style={{
      background: s.bg, border: `1px solid ${s.border}`, borderRadius: 3,
      padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color,
    }}>{source}</span>
  )
}

export default function MediaVault({ isActive = true }) {
  const [images, setImages] = useState([])
  const [stats, setStats] = useState(null)
  const [search, setSearch] = useState('')
  const [dragging, setDragging] = useState(false)
  const [detail, setDetail] = useState(null)
  const [error, setError] = useState(null)
  const [refreshing, setRefreshing] = useState(false)
  const pageRef = useRef(null)

  const load = useCallback(async () => {
    try {
      const [imgs, st] = await Promise.all([
        WailsApp.GetVaultImages(200),
        WailsApp.GetVaultStats(),
      ])
      setImages(imgs || [])
      setStats(st)
    } catch (e) {
      setError('Failed to load vault: ' + e)
    }
  }, [])

  useEffect(() => {
    load()
    const unsub = onImagesChanged(() => {
      load()
    })
    return () => {
      if (typeof unsub === 'function') unsub()
    }
  }, [load])
  useReloadOnActivate(isActive, load)

  const handleRefresh = async () => {
    setRefreshing(true)
    try { await load() } finally { setRefreshing(false) }
  }

  const filtered = search
    ? images.filter(img =>
        img.id.includes(search) ||
        (img.label || '').toLowerCase().includes(search.toLowerCase()) ||
        img.filename.toLowerCase().includes(search.toLowerCase()) ||
        img.source.includes(search)
      )
    : images

  const handlePickAndAdd = async () => {
    setError(null)
    try {
      const path = await WailsApp.OpenVaultFilePicker()
      if (!path) return // user cancelled
      await WailsApp.AddVaultImage(path, '')
      load()
    } catch (e) {
      setError('Upload failed: ' + e)
    }
  }

  const handleDrop = (e) => {
    e.preventDefault()
    setDragging(false)
    const files = Array.from(e.dataTransfer.files).filter(f => /^(image|audio|video)\//.test(f.type))
    if (!files.length) return
    setError(null)
    // Use first dropped file's path (Wails WebKit sets file.path on drop)
    const promises = files.map(async (file) => {
      const path = file.path
      if (!path) {
        setError('Drag-drop path unavailable — use the Add Media button instead')
        return
      }
      await WailsApp.AddVaultImage(path, '')
    })
    Promise.all(promises).then(load).catch(e => setError('Upload failed: ' + e))
  }

  const handleDelete = async (id) => {
    setImages(prev => prev.filter(img => img.id !== id))
    try {
      const st = await WailsApp.GetVaultStats()
      setStats(st)
    } catch (_) {}
  }

  return (
    <div
      ref={pageRef}
      onDragOver={(e) => { e.preventDefault(); setDragging(true) }}
      onDragLeave={(e) => { if (!pageRef.current?.contains(e.relatedTarget)) setDragging(false) }}
      onDrop={handleDrop}
      style={{
        display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden',
        outline: dragging ? '2px dashed #00b4d8' : 'none',
        outlineOffset: -3,
      }}
    >
      {/* Header */}
      <div style={{
        padding: '14px 20px 10px', borderBottom: '1px solid #0d1a26',
        display: 'flex', alignItems: 'center', gap: 12,
      }}>
        <div>
          <div style={{ color: '#e2e8f0', fontSize: 16, fontWeight: 600 }}>Media Vault</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>
            {stats ? `${stats.count} items · ${fmtBytes(stats.total_bytes)}` : 'loading…'}
          </div>
        </div>
        <div style={{ flex: 1 }} />
        <div style={{ position: 'relative' }}>
          <Search size={11} style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: '#475569' }} />
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="search…"
            style={{
              background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 5,
              padding: '5px 8px 5px 26px', color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 11, width: 160,
            }}
          />
        </div>
        <RefreshButton onClick={handleRefresh} loading={refreshing} />
        <button
          onClick={handlePickAndAdd}
          style={{
            background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
            borderRadius: 6, padding: '6px 12px', color: '#00b4d8',
            fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
            display: 'flex', alignItems: 'center', gap: 5,
          }}
        >
          <Plus size={12} /> Add Media
        </button>
      </div>

      {/* Drop hint */}
      <div style={{
        padding: '4px 20px', background: dragging ? 'rgba(0,180,216,0.06)' : '#060b11',
        borderBottom: '1px solid #0a1520',
        fontFamily: 'var(--font-mono)', fontSize: 9,
        color: dragging ? '#00b4d8' : '#1e3a4f', textAlign: 'center',
        transition: 'all 0.15s',
      }}>
        {dragging ? 'Drop to add to vault' : 'Drop images, audio or video anywhere to add them to the vault'}
      </div>

      {/* Error */}
      {error && (
        <div style={{ margin: '8px 20px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
          {error}
        </div>
      )}

      {/* Column headers */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 0,
        padding: '5px 20px', borderBottom: '1px solid #0a1520',
        fontFamily: 'var(--font-mono)', fontSize: 9, color: '#334155',
        letterSpacing: '1px', textTransform: 'uppercase',
      }}>
        <div style={{ width: 44 }} />
        <div style={{ width: 72 }}>ID</div>
        <div style={{ flex: 1 }}>Label / Filename</div>
        <div style={{ width: 80 }}>Source</div>
        <div style={{ width: 120, overflow: 'hidden' }}>Workflow</div>
        <div style={{ width: 56 }}>Size</div>
        <div style={{ width: 56 }}>Date</div>
        <div style={{ width: 28 }} />
      </div>

      {/* Rows */}
      <div style={{ flex: 1, overflowY: 'auto' }}>
        {filtered.length === 0 && (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            height: 200, gap: 12, color: '#334155',
          }}>
            <ImageIcon size={32} style={{ opacity: 0.3 }} />
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              {search ? 'Nothing matches your search' : 'Nothing here yet — run a workflow or drop images, audio or video here'}
            </div>
          </div>
        )}
        {filtered.map(img => (
          <div
            key={img.id}
            onClick={() => setDetail(img)}
            style={{
              display: 'flex', alignItems: 'center', gap: 0,
              padding: '6px 20px', borderBottom: '1px solid #0a1520',
              cursor: 'pointer',
              transition: 'background 0.1s',
            }}
            onMouseEnter={e => e.currentTarget.style.background = '#0d1f35'}
            onMouseLeave={e => e.currentTarget.style.background = ''}
          >
            <div style={{ width: 44, paddingRight: 8 }}>
              <div style={{
                width: 36, height: 36, borderRadius: 4, overflow: 'hidden',
                background: '#111827', border: '1px solid #1e3a4f', flexShrink: 0,
              }}>
                {isVaultImage(img) ? <VaultThumb id={img.id} /> : <FileThumb item={img} />}
              </div>
            </div>
            <div style={{ width: 72, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#00b4d8', fontWeight: 600 }}>{img.id}</div>
            <div style={{ flex: 1, minWidth: 0, paddingRight: 10 }}>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {img.label || '—'}
              </div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: '#334155', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {img.filename}
              </div>
            </div>
            <div style={{ width: 80 }}>{sourceBadge(img.source)}</div>
            <div style={{ width: 120, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 8 }}>
              {img.workflow_id || '—'}
            </div>
            <div style={{ width: 56, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>{fmtBytes(img.size_bytes)}</div>
            <div style={{ width: 56, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>{fmtDate(img.created_at)}</div>
            <div style={{ width: 28 }}>
              <button
                onClick={async (e) => {
                  e.stopPropagation()
                  try {
                    await WailsApp.DeleteVaultImage(img.id)
                    setImages(prev => prev.filter(i => i.id !== img.id))
                    try {
                      const st = await WailsApp.GetVaultStats()
                      setStats(st)
                    } catch (_) {}
                  } catch (err) { setError('Delete failed: ' + err) }
                }}
                style={{
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: '#4b5563', padding: 4, borderRadius: 3,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                }}
                onMouseEnter={e => e.currentTarget.style.color = '#ef4444'}
                onMouseLeave={e => e.currentTarget.style.color = '#4b5563'}
              >
                <Trash2 size={13} />
              </button>
            </div>
          </div>
        ))}
      </div>

      {/* Detail modal */}
      {detail && (
        <ImageDetailModal
          image={detail}
          onClose={() => setDetail(null)}
          onDelete={handleDelete}
          onSaved={load}
          onRename={(img) => {
            setDetail({ ...detail, label: img.label })
            load()
          }}
        />
      )}

    </div>
  )
}
