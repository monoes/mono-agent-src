import { useState, useEffect } from 'react'
import { X, Wand2, Download, Loader2, CheckCircle, AlertCircle, Sliders, Crop, RefreshCw, FileImage } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'

const TABS = [
  { id: 'resize',     label: 'Resize',     icon: RefreshCw },
  { id: 'crop',       label: 'Crop',       icon: Crop },
  { id: 'adjust',     label: 'Adjust',     icon: Sliders },
  { id: 'convert',    label: 'Convert',    icon: FileImage },
]

const inputStyle = {
  background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '5px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)',
  fontSize: 11, width: '100%', boxSizing: 'border-box',
}

const labelStyle = {
  fontFamily: 'var(--font-mono)', fontSize: 10, color: '#64748b',
  textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4, display: 'block',
}

// <select>s take their look from the global `select` rule plus
// `.select-compact` (index.css); only layout goes inline, and never
// inputStyle's `background` shorthand, which would wipe the chevron.
const selectStyle = { width: '100%', fontSize: 11 }

function Field({ label, children }) {
  return (
    <div style={{ marginBottom: 12 }}>
      <span style={labelStyle}>{label}</span>
      {children}
    </div>
  )
}

function NumberInput({ value, onChange, min, max, step = 1 }) {
  return (
    <input
      type="number"
      value={value}
      min={min}
      max={max}
      step={step}
      onChange={e => onChange(Number(e.target.value))}
      style={inputStyle}
    />
  )
}

function SliderInput({ value, onChange, min, max, step = 0.05 }) {
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
      <input
        type="range"
        value={value}
        min={min}
        max={max}
        step={step}
        onChange={e => onChange(Number(e.target.value))}
        style={{ flex: 1, accentColor: '#00b4d8' }}
      />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#94a3b8', minWidth: 32, textAlign: 'right' }}>
        {value}
      </span>
    </div>
  )
}

// ── Tab panels ────────────────────────────────────────────────────────────────

function ResizePanel({ onApply, busy }) {
  const [width, setWidth]   = useState(800)
  const [height, setHeight] = useState(600)
  const [fit, setFit]       = useState('contain')
  return (
    <div>
      <Field label="Width (px)"><NumberInput value={width} onChange={setWidth} min={1} max={8000} /></Field>
      <Field label="Height (px)"><NumberInput value={height} onChange={setHeight} min={1} max={8000} /></Field>
      <Field label="Fit mode">
        <select value={fit} onChange={e => setFit(e.target.value)} className="select-compact" style={selectStyle}>
          {['contain','cover','fill','width','height'].map(f => <option key={f} value={f}>{f}</option>)}
        </select>
      </Field>
      <ApplyButton busy={busy} onClick={() =>
        onApply('image.resize', { field: 'image_path', output_field: 'out_path', width, height, fit })
      } />
    </div>
  )
}

function CropPanel({ onApply, busy }) {
  const [x, setX]         = useState(0)
  const [y, setY]         = useState(0)
  const [width, setWidth]   = useState(400)
  const [height, setHeight] = useState(400)
  return (
    <div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, marginBottom: 12 }}>
        <Field label="X"><NumberInput value={x} onChange={setX} min={0} /></Field>
        <Field label="Y"><NumberInput value={y} onChange={setY} min={0} /></Field>
        <Field label="Width (px)"><NumberInput value={width} onChange={setWidth} min={1} /></Field>
        <Field label="Height (px)"><NumberInput value={height} onChange={setHeight} min={1} /></Field>
      </div>
      <ApplyButton busy={busy} onClick={() =>
        onApply('image.crop', { field: 'image_path', output_field: 'out_path', x, y, width, height })
      } />
    </div>
  )
}

function AdjustPanel({ onApply, busy }) {
  const [brightness, setBrightness] = useState(1)
  const [contrast, setContrast]     = useState(1)
  const [saturation, setSaturation] = useState(1)
  const [sharpness, setSharpness]   = useState(0)
  const [blur, setBlur]             = useState(0)
  const [grayscale, setGrayscale]   = useState(false)
  const [sepia, setSepia]           = useState(false)
  const [invert, setInvert]         = useState(false)
  return (
    <div>
      <Field label={`Brightness — ${brightness}`}><SliderInput value={brightness} onChange={setBrightness} min={0} max={3} /></Field>
      <Field label={`Contrast — ${contrast}`}><SliderInput value={contrast} onChange={setContrast} min={0} max={3} /></Field>
      <Field label={`Saturation — ${saturation}`}><SliderInput value={saturation} onChange={setSaturation} min={0} max={3} /></Field>
      <Field label={`Sharpness — ${sharpness}`}><SliderInput value={sharpness} onChange={setSharpness} min={0} max={10} step={0.5} /></Field>
      <Field label={`Blur — ${blur}`}><SliderInput value={blur} onChange={setBlur} min={0} max={20} step={0.5} /></Field>
      <div style={{ display: 'flex', gap: 16, marginBottom: 16 }}>
        {[['Grayscale', grayscale, setGrayscale], ['Sepia', sepia, setSepia], ['Invert', invert, setInvert]].map(([lbl, val, set]) => (
          <label key={lbl} style={{ display: 'flex', alignItems: 'center', gap: 5, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', cursor: 'pointer' }}>
            <input type="checkbox" checked={val} onChange={e => set(e.target.checked)} style={{ accentColor: '#00b4d8' }} />
            {lbl}
          </label>
        ))}
      </div>
      <ApplyButton busy={busy} onClick={() =>
        onApply('image.adjust', { field: 'image_path', output_field: 'out_path', brightness, contrast, saturation, sharpness, blur, grayscale, sepia, invert })
      } />
    </div>
  )
}

function ConvertPanel({ onApply, busy }) {
  const [format, setFormat]   = useState('jpeg')
  const [quality, setQuality] = useState(90)
  return (
    <div>
      <Field label="Output format">
        <select value={format} onChange={e => setFormat(e.target.value)} className="select-compact" style={selectStyle}>
          {['jpeg','png','gif','tiff','bmp'].map(f => <option key={f} value={f}>{f.toUpperCase()}</option>)}
        </select>
      </Field>
      {(format === 'jpeg') && (
        <Field label={`Quality — ${quality}`}><SliderInput value={quality} onChange={setQuality} min={1} max={100} step={1} /></Field>
      )}
      <ApplyButton busy={busy} onClick={() =>
        onApply('image.convert', { field: 'image_path', output_field: 'out_path', format, quality })
      } />
    </div>
  )
}

function ApplyButton({ busy, onClick }) {
  return (
    <button
      onClick={onClick}
      disabled={busy}
      style={{
        background: busy ? 'rgba(0,180,216,0.05)' : 'rgba(0,180,216,0.12)',
        border: '1px solid rgba(0,180,216,0.4)', borderRadius: 6,
        padding: '8px 18px', color: busy ? '#475569' : '#00b4d8',
        fontFamily: 'var(--font-mono)', fontSize: 12, cursor: busy ? 'not-allowed' : 'pointer',
        display: 'flex', alignItems: 'center', gap: 7, width: '100%', justifyContent: 'center',
        transition: 'all 0.15s',
      }}
    >
      {busy
        ? <><Loader2 size={13} style={{ animation: 'spin 1s linear infinite' }} /> Processing…</>
        : <><Wand2 size={13} /> Apply</>
      }
    </button>
  )
}

// ── Main component ────────────────────────────────────────────────────────────

export default function ImageEditorPanel({ image, onClose, onSaved }) {
  const [tab, setTab]           = useState('resize')
  const [busy, setBusy]         = useState(false)
  const [result, setResult]     = useState(null)   // { path, dataUrl }
  const [error, setError]       = useState(null)
  const [saving, setSaving]     = useState(false)
  const [saved, setSaved]       = useState(false)
  const [srcDataUrl, setSrcDataUrl] = useState(null)

  useEffect(() => {
    setResult(null); setError(null); setSaved(false)
    if (!image) return
    try {
      WailsApp.GetVaultImageData(image.id).then(setSrcDataUrl).catch(() => {})
    } catch (_) {}
  }, [image?.id, tab])

  if (!image) return null

  const handleApply = async (nodeType, config) => {
    setBusy(true)
    setResult(null)
    setError(null)
    setSaved(false)
    // Output must go outside the vault dir so AddVaultImage can copy it in
    const configWithDir = { ...config, output_dir: '/tmp/monoagent-edits' }
    try {
      const res = await WailsApp.RunNode({
        node_type: nodeType,
        config: configWithDir,
        items: [{ image_path: image.path }],
      })
      if (res.error) throw new Error(res.error)
      const items = res.outputs?.[0]?.items
      if (!items?.length) throw new Error('No output from node')
      const outPath = items[0].out_path
      if (!outPath) throw new Error('Node did not return an output path')
      // Read back as data URL for preview
      const added = await WailsApp.AddVaultImage(outPath, `${image.label || image.id} [${nodeType.split('.')[1]}]`)
      const dataUrl = await WailsApp.GetVaultImageData(added.id)
      setResult({ path: outPath, dataUrl, vaultId: added.id })
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const handleSaveToVault = async () => {
    if (!result || saved) return
    setSaving(true)
    try {
      // Already added to vault in handleApply — just notify parent
      setSaved(true)
      onSaved?.()
    } catch (e) {
      setError(String(e))
    } finally {
      setSaving(false)
    }
  }

  const ActivePanel = {
    resize:    ResizePanel,
    crop:      CropPanel,
    adjust:    AdjustPanel,
    convert:   ConvertPanel,
  }[tab]

  return (
    <>
      {/* spin keyframe */}
      <style>{`@keyframes spin{from{transform:rotate(0)}to{transform:rotate(360deg)}}`}</style>

      {/* Backdrop */}
      <div
        onClick={onClose}
        style={{
          position: 'fixed', inset: 0, background: 'rgba(4,6,10,0.5)', zIndex: 200,
        }}
      />

      {/* Panel */}
      <div style={{
        position: 'fixed', top: 0, right: 0, bottom: 0,
        width: 360, background: '#080c14',
        borderLeft: '1px solid #0d1a26',
        display: 'flex', flexDirection: 'column',
        zIndex: 201, overflow: 'hidden',
        boxShadow: '-8px 0 40px rgba(0,0,0,0.6)',
      }}>

        {/* Header */}
        <div style={{
          padding: '14px 16px', borderBottom: '1px solid #0d1a26',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ color: '#e2e8f0', fontSize: 13, fontWeight: 600 }}>Edit Image</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {image.label || image.filename}
            </div>
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 4, display: 'flex' }}>
            <X size={16} />
          </button>
        </div>

        {/* Source image preview */}
        <div style={{
          margin: '12px 16px', borderRadius: 6, overflow: 'hidden',
          background: '#0d1a26', border: '1px solid #1e3a4f',
          height: 130, display: 'flex', alignItems: 'center', justifyContent: 'center',
          flexShrink: 0,
        }}>
          {srcDataUrl
            ? <img src={srcDataUrl} alt="source" style={{ maxWidth: '100%', maxHeight: '100%', objectFit: 'contain' }} />
            : <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#334155' }}>loading…</span>
          }
        </div>

        {/* Tabs */}
        <div style={{
          display: 'flex', borderBottom: '1px solid #0d1a26',
          flexShrink: 0, overflowX: 'auto',
        }}>
          {TABS.map(t => {
            const Icon = t.icon
            const active = tab === t.id
            return (
              <button
                key={t.id}
                onClick={() => { setTab(t.id); setResult(null); setError(null); setSaved(false) }}
                style={{
                  flex: 1, padding: '8px 4px', background: 'none',
                  border: 'none', borderBottom: active ? '2px solid #00b4d8' : '2px solid transparent',
                  color: active ? '#00b4d8' : '#475569', cursor: 'pointer',
                  fontFamily: 'var(--font-mono)', fontSize: 9, textTransform: 'uppercase',
                  display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 4,
                  transition: 'color 0.15s',
                  whiteSpace: 'nowrap',
                }}
              >
                <Icon size={13} />
                {t.label}
              </button>
            )
          })}
        </div>

        {/* Tab body */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '16px' }}>
          <ActivePanel onApply={handleApply} busy={busy} />

          {/* Error */}
          {error && (
            <div style={{
              marginTop: 14, background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)',
              borderRadius: 6, padding: '10px 12px',
              display: 'flex', gap: 8, alignItems: 'flex-start',
            }}>
              <AlertCircle size={13} style={{ color: '#f87171', flexShrink: 0, marginTop: 1 }} />
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>{error}</span>
            </div>
          )}

          {/* Result preview */}
          {result && (
            <div style={{ marginTop: 16 }}>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#64748b', marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                Result
              </div>
              <div style={{
                borderRadius: 6, overflow: 'hidden', background: '#0d1a26',
                border: '1px solid #1e3a4f', marginBottom: 10,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                minHeight: 120,
              }}>
                <img src={result.dataUrl} alt="result" style={{ maxWidth: '100%', maxHeight: 200, objectFit: 'contain' }} />
              </div>

              {saved ? (
                <div style={{
                  display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6,
                  padding: '8px', background: 'rgba(16,185,129,0.08)',
                  border: '1px solid rgba(16,185,129,0.2)', borderRadius: 6,
                  fontFamily: 'var(--font-mono)', fontSize: 11, color: '#34d399',
                }}>
                  <CheckCircle size={13} /> Saved to vault
                </div>
              ) : (
                <button
                  onClick={handleSaveToVault}
                  disabled={saving}
                  style={{
                    width: '100%', padding: '8px', background: 'rgba(16,185,129,0.1)',
                    border: '1px solid rgba(16,185,129,0.3)', borderRadius: 6,
                    color: '#34d399', fontFamily: 'var(--font-mono)', fontSize: 11,
                    cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6,
                  }}
                >
                  <Download size={12} /> Save to Vault
                </button>
              )}
            </div>
          )}
        </div>
      </div>
    </>
  )
}
