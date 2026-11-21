import { createContext, isValidElement, useContext, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SandboxedHtmlFrame } from './SandboxedHtmlFrame.jsx'
import { htmlViewsEnabled } from './htmlViewContract.js'
import { isUserGesture } from './chatLinks.js'

// Hand the chat's callbacks to any html-view block without prop drilling.
// Absent callbacks make send/open no-ops.
export const HtmlViewContext = createContext({})

// Called from ChatMarkdown's `pre` override. Returns an element for a fenced
// `html-view` block when the chat.htmlViews flag is on, else null (the caller
// then renders its ordinary code block).
export function htmlViewForPre(children) {
  const el = Array.isArray(children) ? children[0] : children
  if (!isValidElement(el)) return null
  if (!/(^|\s)language-html-view(\s|$)/.test(String(el.props.className || ''))) return null
  if (!htmlViewsEnabled()) return null
  const c = el.props.children
  const html = (Array.isArray(c) ? c.join('') : String(c ?? '')).replace(/\n$/, '')
  return <HtmlViewBlock html={html} />
}

const note = { display: 'flex', flexDirection: 'column', gap: 6, margin: '0.35em 0', padding: '6px 8px', border: '1px solid #f59e0b', borderRadius: 6, background: '#0a1018', fontSize: 11, color: '#94a3b8' }
const btn = { background: 'none', border: '1px solid #1e3a4f', borderRadius: 4, color: '#00b4d8', cursor: 'pointer', fontSize: 10, padding: '1px 8px' }

// A frame can ask to send a message, but its gesture signal is spoofable, so
// nothing is sent from the frame's say-so: the text is shown here, in the
// host's own DOM, and goes out only on a real click on Send.
function HtmlViewBlock({ html }) {
  const { t } = useTranslation()
  const { onSend, onAppLink } = useContext(HtmlViewContext)
  const [pending, setPending] = useState(null)
  const send = () => {
    const text = pending
    setPending(null)
    if (text) onSend?.(text)
  }
  return (
    <>
      <SandboxedHtmlFrame html={html} onSend={onSend ? setPending : undefined} onAppLink={onAppLink} />
      {pending != null && (
        <div data-testid="html-view-confirm" role="alert" style={note}>
          <span>{t('htmlView.confirm')}</span>
          <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', color: '#f1f5f9', fontFamily: 'var(--font-mono)' }}>{pending}</pre>
          <div style={{ display: 'flex', gap: 6 }}>
            <button type="button" style={btn} onClick={(e) => { if (isUserGesture(e)) send() }}>{t('htmlView.send')}</button>
            <button type="button" style={btn} onClick={() => setPending(null)}>{t('htmlView.cancel')}</button>
          </div>
        </div>
      )}
    </>
  )
}
