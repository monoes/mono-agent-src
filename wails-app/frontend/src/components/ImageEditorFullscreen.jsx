import { useState, useEffect, useRef } from 'react'
import { X, Wand2, Download, Loader2, CheckCircle, AlertCircle, Crop } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { useEscapeClose } from '../lib/escapeStack.js'
import { mono, btnBase, TABS, OUT_DIR, DEFAULT_ADJ, buildFilter, AdjustControls, ResizeControls, CropOverlay, CropControls, ConvertControls } from './ImageEditorParts.jsx'
export { ConvertControls } from './ImageEditorParts.jsx'

export default function ImageEditorFullscreen({ image, onClose, onSaved, initialTab = 'adjust' }) {
  const [tab, setTab]         = useState(initialTab)
  const [srcUrl, setSrcUrl]   = useState(null)
  const [natW, setNatW]       = useState(0)
  const [natH, setNatH]       = useState(0)
  const [adj, setAdj]         = useState({ ...DEFAULT_ADJ })
  const [width, setWidth]     = useState(800)
  const [height, setHeight]   = useState(600)
  const [fit, setFit]         = useState('contain')
  const [cx, setCx]           = useState(0)
  const [cy, setCy]           = useState(0)
  const [cw, setCw]           = useState(0)
  const [ch, setCh]           = useState(0)
  const [format, setFormat]   = useState('jpeg')
  const [quality, setQuality] = useState(85)
  const [busy, setBusy]       = useState(false)
  const [resultUrl, setResultUrl] = useState(null)
  const [error, setError]     = useState(null)
  const [saved, setSaved]     = useState(false)
  const imgRef = useRef(null)

  useEscapeClose(onClose, !!image)

  useEffect(() => {
    if (!image) return
    setSrcUrl(null); setResultUrl(null); setError(null); setSaved(false)
    WailsApp.GetVaultImageData(image.id).then(setSrcUrl).catch(() => {})
  }, [image?.id])

  useEffect(() => { setResultUrl(null); setError(null); setSaved(false) }, [tab])

  // When natural size known, init crop to full image and resize to image size
  const onImgLoad = () => {
    const el = imgRef.current
    if (!el) return
    setNatW(el.naturalWidth); setNatH(el.naturalHeight)
    setCx(0); setCy(0); setCw(el.naturalWidth); setCh(el.naturalHeight)
    setWidth(el.naturalWidth); setHeight(el.naturalHeight)
  }

  const runNode = async (nodeType, config) => {
    setBusy(true); setResultUrl(null); setError(null); setSaved(false)
    try {
      const res = await WailsApp.RunNode({ node_type: nodeType, config, items: [{ image_path: image.path }] })
      if (res.error) throw new Error(res.error)
      const outPath = res.outputs?.[0]?.items?.[0]?.out_path
      if (!outPath) throw new Error('No output path returned')
      const added = await WailsApp.AddVaultImage(outPath, `${image.label || image.id} [${nodeType.split('.')[1]}]`)
      const url   = await WailsApp.GetVaultImageData(added.id)
      setResultUrl(url); onSaved?.()
    } catch (e) { setError(String(e)) }
    finally { setBusy(false) }
  }

  const handleProcess = () => {
    if (tab === 'resize')    return runNode('image.resize',    { field: 'image_path', output_field: 'out_path', output_dir: OUT_DIR, width, height, fit })
    if (tab === 'crop')      return runNode('image.crop',      { field: 'image_path', output_field: 'out_path', output_dir: OUT_DIR, x: cx, y: cy, width: cw, height: ch })
    if (tab === 'convert')   return runNode('image.convert',   { field: 'image_path', output_field: 'out_path', output_dir: OUT_DIR, format, quality })
  }

  const handleSaveAdjust = () => runNode('image.adjust', {
    field: 'image_path', output_field: 'out_path', output_dir: OUT_DIR,
    brightness: adj.brightness, contrast: adj.contrast, saturation: adj.saturation,
    blur: adj.blur, grayscale: adj.grayscale, sepia: adj.sepia, invert: adj.invert,
  })

  const isAdjust = tab === 'adjust'
  const showCropOverlay = tab === 'crop' && natW > 0
  const showResizeOverlay = tab === 'resize' && natW > 0
  const cssFilter = isAdjust ? buildFilter(adj) : 'none'
  const displayUrl = resultUrl || srcUrl

  if (!image) return null

  return (
    <>
      <style>{`@keyframes spin{from{transform:rotate(0)}to{transform:rotate(360deg)}}`}</style>
      <div role="dialog" aria-modal="true" aria-label="Image editor" style={{ position: 'fixed', inset: 0, zIndex: 2000, background: '#060b11', display: 'flex', flexDirection: 'column' }}>

        {/* Top bar */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '11px 20px', borderBottom: '1px solid #0d1a26', background: '#080c14', flexShrink: 0 }}>
          <div style={{ flex: 1 }}>
            <span style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600 }}>Edit Image</span>
            <span style={{ ...mono, fontSize: 10, color: '#475569', marginLeft: 10 }}>{image.label || image.filename}</span>
            {natW > 0 && <span style={{ ...mono, fontSize: 10, color: '#334155', marginLeft: 8 }}>{natW}×{natH}</span>}
          </div>
          <div style={{ display: 'flex', gap: 4 }}>
            {TABS.map(t => {
              const Icon = t.icon; const active = tab === t.id
              return (
                <button key={t.id} onClick={() => setTab(t.id)} style={{
                  background: active ? 'rgba(0,180,216,.15)' : 'none',
                  border: `1px solid ${active ? 'rgba(0,180,216,.4)' : '#1e3a4f'}`,
                  borderRadius: 6, padding: '5px 11px', color: active ? '#00b4d8' : '#475569',
                  cursor: 'pointer', ...mono, fontSize: 10,
                  display: 'flex', alignItems: 'center', gap: 5, transition: 'all .15s',
                }}>
                  <Icon size={11} />{t.label}
                </button>
              )
            })}
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 4, display: 'flex' }}><X size={18} /></button>
        </div>

        {/* Body */}
        <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>

          {/* Image canvas */}
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#040608', overflow: 'hidden', position: 'relative' }}>
            {!srcUrl && <span style={{ ...mono, color: '#334155' }}>Loading…</span>}
            {srcUrl && (
              <img
                ref={imgRef}
                src={displayUrl}
                alt="preview"
                onLoad={onImgLoad}
                style={{
                  maxWidth: '90%', maxHeight: '90%', objectFit: 'contain',
                  filter: cssFilter, transition: 'filter .05s',
                  borderRadius: 4, boxShadow: '0 8px 40px rgba(0,0,0,.6)',
                  display: 'block',
                  // For crop/resize, hide the box-shadow so overlay blends
                  ...(showCropOverlay || showResizeOverlay ? { boxShadow: 'none' } : {}),
                }}
              />
            )}

            {/* Crop overlay */}
            {showCropOverlay && (
              <CropOverlay imgRef={imgRef} natW={natW} natH={natH}
                cx={cx} cy={cy} cw={cw} ch={ch}
                setCx={setCx} setCy={setCy} setCw={setCw} setCh={setCh} />
            )}

            {/* Resize overlay — shows output frame scaled proportionally */}
            {showResizeOverlay && srcUrl && (
              <div style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', pointerEvents: 'none' }}>
                <div style={{
                  border: '2px dashed rgba(0,180,216,.6)', borderRadius: 2,
                  width: `${Math.min(80, 80 * (width / natW))}%`,
                  height: `${Math.min(80, 80 * (height / natH))}%`,
                  position: 'relative',
                }}>
                  <div style={{ position: 'absolute', top: -20, left: '50%', transform: 'translateX(-50%)', background: 'rgba(0,0,0,.8)', border: '1px solid rgba(0,180,216,.5)', borderRadius: 4, padding: '2px 8px', ...mono, fontSize: 10, color: '#00b4d8', whiteSpace: 'nowrap' }}>
                    {width} × {height} px
                  </div>
                </div>
              </div>
            )}

            {busy && (
              <div style={{ position: 'absolute', inset: 0, background: 'rgba(4,6,10,.65)', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 12 }}>
                <Loader2 size={28} style={{ color: '#00b4d8', animation: 'spin 1s linear infinite' }} />
                <span style={{ ...mono, color: '#64748b' }}>Processing via CLI…</span>
              </div>
            )}
          </div>

          {/* Controls sidebar */}
          <div style={{ width: 300, background: '#080c14', borderLeft: '1px solid #0d1a26', display: 'flex', flexDirection: 'column', overflow: 'hidden', flexShrink: 0 }}>
            <div style={{ flex: 1, overflowY: 'auto', padding: 20 }}>
              {tab === 'adjust'    && <AdjustControls adj={adj} setAdj={setAdj} />}
              {tab === 'resize'    && <ResizeControls width={width} setWidth={setWidth} height={height} setHeight={setHeight} fit={fit} setFit={setFit} natW={natW} natH={natH} />}
              {tab === 'crop'      && <CropControls cx={cx} cy={cy} cw={cw} ch={ch} />}
              {tab === 'convert'   && <ConvertControls format={format} setFormat={setFormat} quality={quality} setQuality={setQuality} sizeBytes={image.size_bytes} />}

              {error && (
                <div style={{ marginTop: 14, background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.2)', borderRadius: 6, padding: '10px 12px', display: 'flex', gap: 8, alignItems: 'flex-start' }}>
                  <AlertCircle size={13} style={{ color: '#f87171', flexShrink: 0, marginTop: 1 }} />
                  <span style={{ ...mono, fontSize: 10, color: '#fca5a5' }}>{error}</span>
                </div>
              )}
              {saved && (
                <div style={{ marginTop: 14, background: 'rgba(16,185,129,.08)', border: '1px solid rgba(16,185,129,.2)', borderRadius: 6, padding: '10px 12px', display: 'flex', gap: 7, alignItems: 'center' }}>
                  <CheckCircle size={13} style={{ color: '#34d399' }} />
                  <span style={{ ...mono, fontSize: 10, color: '#34d399' }}>Saved to vault</span>
                </div>
              )}
            </div>

            {/* Action buttons */}
            <div style={{ padding: '14px 20px', borderTop: '1px solid #0d1a26', display: 'flex', flexDirection: 'column', gap: 8 }}>
              {isAdjust ? (
                <button onClick={handleSaveAdjust} disabled={busy || saved}
                  style={{ ...btnBase, background: 'rgba(16,185,129,.12)', border: '1px solid rgba(16,185,129,.4)', color: '#34d399', opacity: busy || saved ? .5 : 1, cursor: busy || saved ? 'not-allowed' : 'pointer' }}>
                  {busy ? <><Loader2 size={13} style={{ animation: 'spin 1s linear infinite' }} /> Saving…</> : saved ? <><CheckCircle size={13} /> Saved to Vault</> : <><Download size={13} /> Save to Vault</>}
                </button>
              ) : (
                <button onClick={handleProcess} disabled={busy}
                  style={{ ...btnBase, background: 'rgba(0,180,216,.12)', border: '1px solid rgba(0,180,216,.4)', color: '#00b4d8', opacity: busy ? .5 : 1, cursor: busy ? 'not-allowed' : 'pointer' }}>
                  {busy ? <><Loader2 size={13} style={{ animation: 'spin 1s linear infinite' }} /> Processing…</> : <><Wand2 size={13} /> Process &amp; Save to Vault</>}
                </button>
              )}
              <button onClick={onClose} style={{ ...btnBase, background: 'none', border: '1px solid #1e3a4f', color: '#475569' }}>
                <X size={13} /> Close
              </button>
            </div>
          </div>
        </div>
      </div>
    </>
  )
}
