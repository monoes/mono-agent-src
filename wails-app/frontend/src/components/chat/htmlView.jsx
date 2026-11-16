import { createContext, isValidElement, useContext } from 'react'
import { SandboxedHtmlFrame } from './SandboxedHtmlFrame.jsx'
import { htmlViewsEnabled } from './htmlViewContract.js'

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

function HtmlViewBlock({ html }) {
  const { onSend, onAppLink } = useContext(HtmlViewContext)
  return <SandboxedHtmlFrame html={html} onSend={onSend} onAppLink={onAppLink} />
}
