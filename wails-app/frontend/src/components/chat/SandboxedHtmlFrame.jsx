import { useEffect, useMemo, useRef, useState } from 'react'
import { buildSrcdoc, createFocusGesture, createHostGate, htmlTooLarge, MIN_HEIGHT } from './htmlViewContract.js'
import { useTranslation } from 'react-i18next'
import i18nOr from '../../lib/i18nOr.js'

const box = { margin: '0.35em 0', border: '1px solid #1e3a4f', borderRadius: 6, overflow: 'hidden', maxWidth: '100%', minWidth: 0 }
const bar = { display: 'flex', alignItems: 'center', gap: 8, padding: '3px 8px', background: '#0a1018', fontSize: 10, color: '#94a3b8' }

// Model-written HTML in a locked-down iframe. sandbox="allow-scripts" only:
// never allow-same-origin (opaque origin, no Wails bindings, no storage) and
// never top-navigation / forms / popups.
export function SandboxedHtmlFrame({ html, onSend, onAppLink }) {
  useTranslation() // renders again on a language change
  const tooLarge = useMemo(() => htmlTooLarge(html), [html])
  const [interactive, setInteractive] = useState(false)
  const [height, setHeight] = useState(MIN_HEIGHT * 3)
  const frameRef = useRef(null)
  const live = interactive && !tooLarge
  const srcDoc = useMemo(() => (live ? buildSrcdoc(html) : ''), [live, html])

  useEffect(() => {
    if (!live) return undefined
    const focus = createFocusGesture({ getFrame: () => frameRef.current })
    const gate = createHostGate({ getFrameWindow: () => frameRef.current?.contentWindow, pointerOver: focus.take })
    const onMessage = (e) => {
      const m = gate(e)
      if (!m) return
      if (m.t === 'resize') setHeight(m.h)
      else if (m.t === 'send') onSend?.(m.text)
      else if (m.t === 'open') onAppLink?.(m.href)
    }
    window.addEventListener('message', onMessage)
    window.addEventListener('blur', focus.onBlur)
    return () => { window.removeEventListener('message', onMessage); window.removeEventListener('blur', focus.onBlur) }
  }, [live, onSend, onAppLink])

  return (
    <div style={box} data-testid="html-view">
      <div style={bar}>
        <span data-testid="html-view-badge">{i18nOr('ui7Chat.html_sandboxed')}</span>
        {tooLarge
          ? <span data-testid="html-view-toolarge">{i18nOr('ui7Chat.html_too_large')}</span>
          : (
            <button type="button" onClick={() => setInteractive((v) => !v)}
              style={{ marginLeft: 'auto', background: 'none', border: '1px solid #1e3a4f', borderRadius: 4, color: '#94a3b8', cursor: 'pointer', fontSize: 10 }}>
              {i18nOr(interactive ? 'ui7Chat.html_show_source' : 'ui7Chat.html_show_view')}
            </button>
          )}
      </div>
      {live ? (
        <div>
          <iframe ref={frameRef} title={i18nOr('ui7Chat.html_frame')} sandbox="allow-scripts" srcDoc={srcDoc}
            referrerPolicy="no-referrer" style={{ display: 'block', width: '100%', height, border: 0, background: '#fff' }} />
        </div>
      ) : (
        <pre data-testid="html-view-source" style={{ margin: 0, padding: 8, overflow: 'auto', background: '#020509', maxHeight: 300 }}>{html}</pre>
      )}
    </div>
  )
}
