import { useState, useEffect, useRef } from 'react'
import { X, Wand2, Download, Loader2, CheckCircle, AlertCircle, Sliders, Crop,
         RefreshCw, Eraser, FileImage, RotateCcw, Lock, Unlock } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { useEscapeClose } from '../lib/escapeStack.js'

// ── shared style tokens ───────────────────────────────────────────────────────
const mono = { fontFamily: 'var(--font-mono)', fontSize: 11 }
const lbl  = { ...mono, fontSize: 10, color: '#64748b', textTransform: 'uppercase', letterSpacing: '.5px', display: 'block', marginBottom: 5 }
const inp  = { background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5, padding: '5px 8px', color: '#e2e8f0', ...mono, width: '100%', boxSizing: 'border-box' }
const btnBase = { borderRadius: 6, padding: '8px 16px', ...mono, cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6, justifyContent: 'center', transition: 'all .15s', border: 'none' }

const TABS = [
  { id: 'adjust',    label: 'Adjust',    icon: Sliders },
  { id: 'remove_bg', label: 'Remove BG', icon: Eraser },
  { id: 'resize',    label: 'Resize',    icon: RefreshCw },
  { id: 'crop',      label: 'Crop',      icon: Crop },
  { id: 'convert',   label: 'Convert',   icon: FileImage },
]

const OUT_DIR = '/tmp/monoagent-edits'

// ── Adjust helpers ────────────────────────────────────────────────────────────
const DEFAULT_ADJ = { brightness: 1, contrast: 1, saturation: 1, blur: 0, grayscale: false, sepia: false, invert: false }

function buildFilter(a) {
  const p = []
  if (a.brightness !== 1) p.push(`brightness(${a.brightness})`)
  if (a.contrast   !== 1) p.push(`contrast(${a.contrast})`)
  if (a.saturation !== 1) p.push(`saturate(${a.saturation})`)
  if (a.blur       >  0)  p.push(`blur(${a.blur}px)`)
  if (a.grayscale)        p.push('grayscale(1)')
  if (a.sepia)            p.push('sepia(1)')
  if (a.invert)           p.push('invert(1)')
  return p.join(' ') || 'none'
}

function AdjSlider({ label: l, value, onChange, min, max, step = 0.05, dflt }) {
  return (
    <div style={{ marginBottom: 12 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
        <span style={lbl}>{l}</span>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <span style={{ ...mono, fontSize: 10, color: '#94a3b8' }}>{value}</span>
          <button onClick={() => onChange(dflt)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#334155', padding: 0, display: 'flex' }}><RotateCcw size={9} /></button>
        </div>
      </div>
      <input type="range" value={value} min={min} max={max} step={step} onChange={e => onChange(+e.target.value)} style={{ width: '100%', accentColor: '#00b4d8' }} />
    </div>
  )
}

function AdjustControls({ adj, setAdj }) {
  const set = k => v => setAdj(a => ({ ...a, [k]: v }))
  return (
    <div>
      <AdjSlider label="Brightness" value={adj.brightness} onChange={set('brightness')} min={0} max={3} dflt={1} />
      <AdjSlider label="Contrast"   value={adj.contrast}   onChange={set('contrast')}   min={0} max={3} dflt={1} />
      <AdjSlider label="Saturation" value={adj.saturation} onChange={set('saturation')} min={0} max={3} dflt={1} />
      <AdjSlider label="Blur (px)"  value={adj.blur}       onChange={set('blur')}       min={0} max={20} step={0.5} dflt={0} />
      <div style={{ display: 'flex', gap: 14, marginTop: 4 }}>
        {[['Grayscale','grayscale'],['Sepia','sepia'],['Invert','invert']].map(([l, k]) => (
          <label key={k} style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer', ...mono, color: adj[k] ? '#00b4d8' : '#64748b' }}>
            <input type="checkbox" checked={adj[k]} onChange={e => set(k)(e.target.checked)} style={{ accentColor: '#00b4d8' }} />{l}
          </label>
        ))}
      </div>
      <p style={{ ...mono, fontSize: 10, color: '#334155', marginTop: 14 }}>Preview is live. <strong style={{ color: '#475569' }}>Save to Vault</strong> applies via CLI.</p>
    </div>
  )
}

// ── Remove BG ─────────────────────────────────────────────────────────────────
export function RemoveBgControls({ bgcolor, setBgcolor }) {
  const [useColor, setUseColor] = useState(false)
  const pickerColor = bgcolor || '#ffffff'

  const handleToggle = () => {
    if (useColor) { setUseColor(false); setBgcolor('') }
    else { setUseColor(true); setBgcolor(pickerColor.replace('#', '')) }
  }

  const handlePicker = e => {
    const hex = e.target.value.replace('#', '')
    setBgcolor(hex)
  }

  const handleText = e => {
    const v = e.target.value.replace(/[^0-9a-fA-F]/g, '').slice(0, 6)
    setBgcolor(v)
  }

  return (
    <div>
      {/* Transparent vs color selector */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
        <button type="button" aria-pressed={!useColor} onClick={() => { setUseColor(false); setBgcolor('') }}
          style={{ font: 'inherit',
            flex: 1, borderRadius: 7, border: `1.5px solid ${!useColor ? '#00b4d8' : '#1e3a4f'}`,
            background: !useColor ? 'rgba(0,180,216,.1)' : '#0d1a26',
            padding: '10px 8px', cursor: 'pointer', textAlign: 'center',
          }}>
          {/* Transparent checkerboard */}
          <div style={{
            width: 28, height: 28, borderRadius: 4, margin: '0 auto 6px',
            backgroundImage: 'linear-gradient(45deg,#334155 25%,transparent 25%),linear-gradient(-45deg,#334155 25%,transparent 25%),linear-gradient(45deg,transparent 75%,#334155 75%),linear-gradient(-45deg,transparent 75%,#334155 75%)',
            backgroundSize: '8px 8px', backgroundPosition: '0 0,0 4px,4px -4px,-4px 0',
            border: '1px solid #1e3a4f',
          }} />
          <span style={{ ...mono, fontSize: 9, color: !useColor ? '#00b4d8' : '#475569' }}>Transparent</span>
        </button>

        <button type="button" aria-pressed={useColor} onClick={() => { setUseColor(true); if (!bgcolor) setBgcolor('ffffff') }}
          style={{ font: 'inherit',
            flex: 1, borderRadius: 7, border: `1.5px solid ${useColor ? '#00b4d8' : '#1e3a4f'}`,
            background: useColor ? 'rgba(0,180,216,.1)' : '#0d1a26',
            padding: '10px 8px', cursor: 'pointer', textAlign: 'center',
          }}>
          <div style={{
            width: 28, height: 28, borderRadius: 4, margin: '0 auto 6px',
            background: bgcolor ? `#${bgcolor}` : '#ffffff',
            border: '1px solid #1e3a4f',
          }} />
          <span style={{ ...mono, fontSize: 9, color: useColor ? '#00b4d8' : '#475569' }}>Color</span>
        </button>
      </div>

      {/* Color picker row — only when color mode is on */}
      {useColor && (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
          <input type="color" value={`#${bgcolor || 'ffffff'}`} onChange={handlePicker}
            style={{ width: 36, height: 36, padding: 2, borderRadius: 5, border: '1px solid #1e3a4f', background: '#0d1a26', cursor: 'pointer', flexShrink: 0 }} />
          <div style={{ flex: 1, position: 'relative' }}>
            <span style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: '#475569', ...mono, fontSize: 11 }}>#</span>
            <input type="text" value={bgcolor} onChange={handleText} maxLength={6} placeholder="ffffff"
              style={{ ...inp, paddingLeft: 20, letterSpacing: 1 }} />
          </div>
        </div>
      )}

      <div style={{ background: '#0a1520', border: '1px solid #1e3a4f', borderRadius: 6, padding: '8px 10px', marginBottom: 4 }}>
        <div style={{ ...mono, fontSize: 9, color: '#34d399', marginBottom: 2 }}>Output: PNG (transparency preserved)</div>
        <div style={{ ...mono, fontSize: 9, color: '#334155' }}>U2-Net AI model · runs locally · no API key</div>
      </div>
    </div>
  )
}

// ── Resize with aspect-ratio lock ─────────────────────────────────────────────
function ResizeControls({ width, setWidth, height, setHeight, fit, setFit, natW, natH }) {
  const [locked, setLocked] = useState(true)
  const ratio = natW && natH ? natW / natH : 1

  const onW = v => { setWidth(v); if (locked && natH) setHeight(Math.round(v / ratio)) }
  const onH = v => { setHeight(v); if (locked && natW) setWidth(Math.round(v * ratio)) }

  const outW = width, outH = height
  const scale = natW ? Math.round((outW / natW) * 100) : '—'

  return (
    <div>
      {/* Output size badge */}
      <div style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 8,
        padding: '12px 14px', marginBottom: 14, textAlign: 'center',
      }}>
        <div style={{ color: '#00b4d8', fontSize: 20, fontWeight: 700, fontFamily: 'var(--font-mono)', letterSpacing: '-0.5px' }}>
          {outW} <span style={{ color: '#334155' }}>×</span> {outH}
        </div>
        <div style={{ ...mono, fontSize: 10, color: '#475569', marginTop: 4 }}>
          output px · {scale}% of original
          {natW && natH ? ` (${natW}×${natH})` : ''}
        </div>
      </div>

      <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end', marginBottom: 12 }}>
        <div style={{ flex: 1 }}>
          <span style={lbl}>Width</span>
          <input type="number" value={width} min={1} max={8000} onChange={e => onW(+e.target.value)} style={inp} />
        </div>
        <button onClick={() => setLocked(l => !l)} title={locked ? 'Unlock ratio' : 'Lock ratio'}
          style={{ background: locked ? 'rgba(0,180,216,.12)' : '#0d1a26', border: `1px solid ${locked ? 'rgba(0,180,216,.4)' : '#1e3a4f'}`, borderRadius: 5, padding: '6px 8px', cursor: 'pointer', color: locked ? '#00b4d8' : '#475569', display: 'flex', marginBottom: 1 }}>
          {locked ? <Lock size={13} /> : <Unlock size={13} />}
        </button>
        <div style={{ flex: 1 }}>
          <span style={lbl}>Height</span>
          <input type="number" value={height} min={1} max={8000} onChange={e => onH(+e.target.value)} style={inp} />
        </div>
      </div>

      <span style={lbl}>Fit mode</span>
      <select value={fit} onChange={e => setFit(e.target.value)} className="select-compact" style={{ width: '100%', fontSize: 11 }}>
        {['contain','cover','fill','width','height'].map(f => (
          <option key={f} value={f}>{f} {f==='contain'?'— letterbox':f==='cover'?'— crop to fill':f==='fill'?'— stretch':''}</option>
        ))}
      </select>

      {/* Visual size comparison */}
      {natW && natH && (
        <div style={{ marginTop: 14 }}>
          <span style={lbl}>Size preview</span>
          <div style={{ background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 6, padding: 10, position: 'relative', height: 60, overflow: 'hidden' }}>
            {/* Original outline */}
            <div style={{
              position: 'absolute', border: '1px dashed #334155', borderRadius: 2,
              width: Math.min(240, 240), height: Math.min(50, 50 * (natH / natW) * (240 / natW) || 50),
              left: 10, top: '50%', transform: 'translateY(-50%)',
            }} />
            {/* Output scaled */}
            <div style={{
              position: 'absolute', background: 'rgba(0,180,216,.18)', border: '1px solid rgba(0,180,216,.5)', borderRadius: 2,
              width: Math.min(240, 240 * (outW / natW)),
              height: Math.min(50, 50 * (outH / natH)),
              left: 10, top: '50%', transform: 'translateY(-50%)',
            }} />
            <span style={{ ...mono, fontSize: 9, color: '#475569', position: 'absolute', bottom: 4, right: 8 }}>blue = output</span>
          </div>
        </div>
      )}
    </div>
  )
}

// ── Visual Crop Overlay ───────────────────────────────────────────────────────
function CropOverlay({ imgRef, natW, natH, cx, cy, cw, ch, setCx, setCy, setCw, setCh }) {
  const overlayRef  = useRef(null)
  const dragState   = useRef(null)
  const moveHandler = useRef(null)
  const upHandler   = useRef(null)

  // Compute display-space selection rect relative to the overlay div
  const toDisplay = () => {
    if (!imgRef.current || !overlayRef.current) return { left: 0, top: 0, width: 0, height: 0 }
    const ir = imgRef.current.getBoundingClientRect()
    const or = overlayRef.current.getBoundingClientRect()
    const scaleX = ir.width  / natW
    const scaleY = ir.height / natH
    return {
      left:   (ir.left - or.left) + cx * scaleX,
      top:    (ir.top  - or.top)  + cy * scaleY,
      width:  cw * scaleX,
      height: ch * scaleY,
    }
  }

  const startDrag = (e, type) => {
    e.preventDefault(); e.stopPropagation()
    dragState.current = { type, startX: e.clientX, startY: e.clientY, initCx: cx, initCy: cy, initCw: cw, initCh: ch }

    moveHandler.current = (ev) => {
      const ds = dragState.current
      if (!ds || !imgRef.current) return
      const ir = imgRef.current.getBoundingClientRect()
      const sx = natW / ir.width, sy = natH / ir.height
      const dx = (ev.clientX - ds.startX) * sx
      const dy = (ev.clientY - ds.startY) * sy

      if (ds.type === 'move') {
        setCx(Math.max(0, Math.min(natW - ds.initCw, Math.round(ds.initCx + dx))))
        setCy(Math.max(0, Math.min(natH - ds.initCh, Math.round(ds.initCy + dy))))
      } else if (ds.type === 'new') {
        const ox = Math.round((ds.startX - ir.left) * sx)
        const oy = Math.round((ds.startY - ir.top)  * sy)
        const ex = Math.round((ev.clientX - ir.left) * sx)
        const ey = Math.round((ev.clientY - ir.top)  * sy)
        setCx(Math.max(0, Math.min(ox, ex)))
        setCy(Math.max(0, Math.min(oy, ey)))
        setCw(Math.max(1, Math.min(Math.abs(ex - ox), natW)))
        setCh(Math.max(1, Math.min(Math.abs(ey - oy), natH)))
      } else {
        let nx = ds.initCx, ny = ds.initCy, nw = ds.initCw, nh = ds.initCh
        if (ds.type.includes('e')) nw = Math.max(10, ds.initCw + dx)
        if (ds.type.includes('s')) nh = Math.max(10, ds.initCh + dy)
        if (ds.type.includes('w')) { nx = ds.initCx + dx; nw = Math.max(10, ds.initCw - dx) }
        if (ds.type.includes('n')) { ny = ds.initCy + dy; nh = Math.max(10, ds.initCh - dy) }
        setCx(Math.round(Math.max(0, nx))); setCy(Math.round(Math.max(0, ny)))
        setCw(Math.round(Math.min(natW - Math.max(0, nx), nw)))
        setCh(Math.round(Math.min(natH - Math.max(0, ny), nh)))
      }
    }
    upHandler.current = () => {
      dragState.current = null
      window.removeEventListener('mousemove', moveHandler.current)
      window.removeEventListener('mouseup',   upHandler.current)
    }
    window.addEventListener('mousemove', moveHandler.current)
    window.addEventListener('mouseup',   upHandler.current)
  }

  if (!natW || !natH) return null
  const d = toDisplay()

  const handles = [
    { type: 'n',  style: { top: -5,    left: '50%', transform: 'translateX(-50%)', cursor: 'n-resize'  } },
    { type: 's',  style: { bottom: -5, left: '50%', transform: 'translateX(-50%)', cursor: 's-resize'  } },
    { type: 'w',  style: { left: -5,   top: '50%',  transform: 'translateY(-50%)', cursor: 'w-resize'  } },
    { type: 'e',  style: { right: -5,  top: '50%',  transform: 'translateY(-50%)', cursor: 'e-resize'  } },
    { type: 'nw', style: { top: -5,    left: -5,    cursor: 'nw-resize' } },
    { type: 'ne', style: { top: -5,    right: -5,   cursor: 'ne-resize' } },
    { type: 'sw', style: { bottom: -5, left: -5,    cursor: 'sw-resize' } },
    { type: 'se', style: { bottom: -5, right: -5,   cursor: 'se-resize' } },
  ]

  return (
    <div ref={overlayRef} onMouseDown={e => startDrag(e, 'new')}
      style={{ position: 'absolute', inset: 0, cursor: 'crosshair', userSelect: 'none' }}>

      {/* Dark vignette outside selection — 4 strips */}
      <div style={{ position: 'absolute', left: 0, right: 0, top: 0, height: d.top, background: 'rgba(0,0,0,.55)', pointerEvents: 'none' }} />
      <div style={{ position: 'absolute', left: 0, right: 0, top: d.top + d.height, bottom: 0, background: 'rgba(0,0,0,.55)', pointerEvents: 'none' }} />
      <div style={{ position: 'absolute', left: 0, width: d.left, top: d.top, height: d.height, background: 'rgba(0,0,0,.55)', pointerEvents: 'none' }} />
      <div style={{ position: 'absolute', left: d.left + d.width, right: 0, top: d.top, height: d.height, background: 'rgba(0,0,0,.55)', pointerEvents: 'none' }} />

      {/* Selection box */}
      <div onMouseDown={e => startDrag(e, 'move')}
        style={{ position: 'absolute', left: d.left, top: d.top, width: d.width, height: d.height,
          border: '1.5px solid #00b4d8', cursor: 'move', boxSizing: 'border-box' }}>

        {/* Rule-of-thirds grid */}
        {[33, 66].map(p => (
          <div key={`v${p}`} style={{ position: 'absolute', left: `${p}%`, top: 0, bottom: 0, width: 1, background: 'rgba(0,180,216,.25)', pointerEvents: 'none' }} />
        ))}
        {[33, 66].map(p => (
          <div key={`h${p}`} style={{ position: 'absolute', top: `${p}%`, left: 0, right: 0, height: 1, background: 'rgba(0,180,216,.25)', pointerEvents: 'none' }} />
        ))}

        {/* Resize handles */}
        {handles.map(h => (
          <div key={h.type} onMouseDown={e => startDrag(e, h.type)}
            style={{ position: 'absolute', width: 9, height: 9, background: '#00b4d8', borderRadius: 2, ...h.style }} />
        ))}

        {/* Dimension badge */}
        <div style={{
          position: 'absolute', bottom: d.height > 40 ? 4 : -24, right: 4,
          background: 'rgba(0,0,0,.82)', border: '1px solid rgba(0,180,216,.6)', borderRadius: 4,
          padding: '2px 7px', whiteSpace: 'nowrap', pointerEvents: 'none',
          ...mono, fontSize: 10, color: '#00b4d8',
        }}>
          {cw} × {ch} px
        </div>
      </div>
    </div>
  )
}

function CropControls({ cx, cy, cw, ch }) {
  return (
    <div>
      <p style={{ ...mono, fontSize: 10, color: '#64748b', marginBottom: 10 }}>
        Drag on the image to select a crop region. Use the handles to resize.
      </p>
      <div style={{ background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 8, padding: '12px 14px', textAlign: 'center', marginBottom: 10 }}>
        <div style={{ color: '#00b4d8', fontSize: 18, fontWeight: 700, fontFamily: 'var(--font-mono)' }}>
          {cw} <span style={{ color: '#334155' }}>×</span> {ch} <span style={{ color: '#475569', fontSize: 12 }}>px</span>
        </div>
        <div style={{ ...mono, fontSize: 10, color: '#475569', marginTop: 4 }}>
          offset {cx}, {cy}
        </div>
      </div>
    </div>
  )
}

// ── Convert ───────────────────────────────────────────────────────────────────
const FORMAT_INFO = {
  jpeg: { ext: '.jpg', color: '#f59e0b', note: 'Lossy — smallest size, great for photos' },
  png:  { ext: '.png', color: '#3b82f6', note: 'Lossless — large size, supports transparency' },
  gif:  { ext: '.gif', color: '#8b5cf6', note: 'Lossless — 256 colors, supports animation' },
  tiff: { ext: '.tif', color: '#10b981', note: 'Lossless — very large, used in print' },
  bmp:  { ext: '.bmp', color: '#ec4899', note: 'Uncompressed — largest file size' },
}

export function ConvertControls({ format, setFormat, quality, setQuality, sizeBytes }) {
  const fi = FORMAT_INFO[format]
  const estSize = sizeBytes ? (
    format === 'jpeg' ? Math.round(sizeBytes * (quality / 100) * 0.15) :
    format === 'png'  ? Math.round(sizeBytes * 0.6) :
    format === 'bmp'  ? Math.round(sizeBytes * 3) :
    sizeBytes
  ) : null

  const fmtBytes = b => b < 1024 ? b + ' B' : b < 1024*1024 ? (b/1024).toFixed(0)+' KB' : (b/1024/1024).toFixed(1)+' MB'

  return (
    <div>
      {/* Format cards */}
      <span style={lbl}>Output Format</span>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6, marginBottom: 14 }}>
        {Object.entries(FORMAT_INFO).map(([f, info]) => (
          <button type="button" key={f} aria-pressed={format === f} onClick={() => setFormat(f)} style={{ font: 'inherit',
            border: `1px solid ${format === f ? info.color : '#1e3a4f'}`,
            background: format === f ? `${info.color}18` : '#0d1a26',
            borderRadius: 6, padding: '8px 10px', cursor: 'pointer',
            display: 'flex', alignItems: 'center', gap: 8, transition: 'all .12s',
          }}>
            <span style={{ ...mono, fontSize: 11, fontWeight: 700, color: info.color }}>{f.toUpperCase()}</span>
            <span style={{ ...mono, fontSize: 9, color: '#475569' }}>{info.ext}</span>
          </button>
        ))}
      </div>

      {/* Selected format info */}
      <div style={{ background: '#0d1a26', border: `1px solid ${fi.color}33`, borderRadius: 6, padding: '10px 12px', marginBottom: 14 }}>
        <div style={{ ...mono, fontSize: 10, color: fi.color, marginBottom: 4 }}>{fi.ext}</div>
        <div style={{ ...mono, fontSize: 10, color: '#64748b' }}>{fi.note}</div>
        {estSize && sizeBytes && (
          <div style={{ ...mono, fontSize: 10, color: '#475569', marginTop: 6 }}>
            Est. size: <span style={{ color: '#94a3b8' }}>{fmtBytes(estSize)}</span>
            <span style={{ color: '#334155' }}> (from {fmtBytes(sizeBytes)})</span>
          </div>
        )}
      </div>

      {format === 'jpeg' && (
        <>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
            <span style={lbl}>Quality</span>
            <span style={{ ...mono, fontSize: 10, color: quality >= 80 ? '#34d399' : quality >= 50 ? '#f59e0b' : '#f87171' }}>{quality}%</span>
          </div>
          <input type="range" value={quality} min={1} max={100} step={1} onChange={e => setQuality(+e.target.value)} style={{ width: '100%', accentColor: '#00b4d8' }} />
        </>
      )}
    </div>
  )
}

// ── Main ──────────────────────────────────────────────────────────────────────
export default function ImageEditorFullscreen({ image, onClose, onSaved, initialTab = 'adjust' }) {
  const [tab, setTab]         = useState(initialTab)
  const [srcUrl, setSrcUrl]   = useState(null)
  const [natW, setNatW]       = useState(0)
  const [natH, setNatH]       = useState(0)
  const [adj, setAdj]         = useState({ ...DEFAULT_ADJ })
  const [bgcolor, setBgcolor] = useState('')
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
    if (tab === 'remove_bg') return runNode('image.remove_background', { field: 'image_path', output_field: 'out_path', output_dir: OUT_DIR, output_format: 'png', bgcolor: bgcolor || '' })
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
              {tab === 'remove_bg' && <RemoveBgControls bgcolor={bgcolor} setBgcolor={setBgcolor} />}
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
