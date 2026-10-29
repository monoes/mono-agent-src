import { createContext, useContext, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { ImageOff, Copy, Check } from 'lucide-react'
import { api } from '../../services/api.js'
import { classifyUrl, disguisedHost, codeLooksLikePath, remarkChatPaths } from './chatLinks.js'
import { copyToClipboard } from './toolCardUtils.js'
import { ChatPathChip } from './ChatPathChip.jsx'
import ErrorBoundary from '../ErrorBoundary.jsx'
import { markdownTooDeep } from './markdownGuard.js'

// Only http(s) URLs without credentials ever open (see classifyUrl); anything
// else (javascript:, data:, file:, mailto:, custom schemes, schemeless) is
// rendered inert. App.OpenURL does no validation itself, so this is the app's
// own gate. Parsed with NO base so a relative href can never resolve into an
// allowed scheme it never had: the value checked is the value opened.
export function isAllowedURL(href) {
  return classifyUrl(href).ok
}

function openApproved(url) {
  const c = classifyUrl(url)
  if (c.ok) api.openURL(c.url)
}

const InPre = createContext(false)

function MdLink({ href, children }) {
  const { t } = useTranslation()
  const c = classifyUrl(href)
  if (!c.ok) {
    return (
      <span title={t('chatLinks.blocked')} style={{ color: '#94a3b8', textDecoration: 'underline dotted', cursor: 'default' }}>
        {children}
      </span>
    )
  }
  const text = typeof children === 'string' ? children : Array.isArray(children) && children.every((x) => typeof x === 'string') ? children.join('') : ''
  const fake = disguisedHost(text, href)
  const go = (e) => { e.preventDefault(); openApproved(href) }
  return (
    <>
      <a href={href} title={`${t('chatLinks.openInBrowser')}: ${c.url}`} onClick={go} onAuxClick={go}
        style={{ color: '#00b4d8', textDecoration: 'underline' }}>
        {children}
      </a>
      {fake && <span style={{ color: '#f59e0b', fontSize: '0.9em' }} title={t('chatLinks.disguised', { shown: fake, host: c.host })}> ({c.host})</span>}
    </>
  )
}

function MdPre({ children, ...rest }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const ref = useRef(null)
  const copy = () => {
    copyToClipboard(ref.current?.textContent || '')
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  // overflow:auto bounds a huge single code line to this block's own
  // scrollbar rather than blowing out the panel's layout.
  return (
    <div style={{ position: 'relative', margin: '0.35em 0', maxWidth: '100%' }}>
      <pre ref={ref} style={{ background: '#020509', border: '1px solid #1e3a4f', borderRadius: 6, padding: 8, overflow: 'auto', margin: 0, maxWidth: '100%' }} {...rest}>
        <InPre.Provider value>{children}</InPre.Provider>
      </pre>
      <button type="button" onClick={copy} title={t('chatLinks.copyCode')} aria-label={t('chatLinks.copyCode')}
        style={{ position: 'absolute', top: 4, right: 4, display: 'inline-flex', alignItems: 'center', gap: 3, background: 'rgba(10,16,24,0.85)', border: '1px solid #1e3a4f', borderRadius: 4, padding: '1px 5px', cursor: 'pointer', color: copied ? '#34d399' : '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 9 }}>
        {copied ? <Check size={10} /> : <Copy size={10} />}{copied ? t('chatLinks.copied') : t('chatLinks.copy')}
      </button>
    </div>
  )
}

const codeStyle = { background: '#020509', padding: '1px 4px', borderRadius: 3 }

function makeComponents(noPaths) {
  return {
    p: (p) => <p style={{ margin: '0.35em 0' }} {...p} />,
    a: ({ href, children }) => <MdLink href={href}>{children}</MdLink>,
    // Never auto-fetch remote Markdown images (plan: "Do not auto-fetch
    // remote Markdown images") — no <img> is ever rendered, so the browser
    // never issues the request itself. A same-scheme-gated link lets the
    // user open it explicitly instead.
    img: ({ src, alt }) => (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '1px 6px', border: '1px dashed rgba(148,163,184,0.4)', borderRadius: 4, color: '#94a3b8', fontSize: 10, verticalAlign: 'middle' }}>
        <ImageOff size={11} />
        {alt || 'image'}
        {isAllowedURL(src) && (
          <a href={src} title={`Open in browser: ${src}`} onClick={(e) => { e.preventDefault(); openApproved(src) }} style={{ color: '#00b4d8' }}>open</a>
        )}
      </span>
    ),
    ul: (p) => <ul style={{ margin: '0.3em 0', paddingLeft: 18 }} {...p} />,
    ol: (p) => <ol style={{ margin: '0.3em 0', paddingLeft: 18 }} {...p} />,
    li: (p) => <li style={{ margin: '0.1em 0' }} {...p} />,
    strong: (p) => <strong style={{ color: '#f1f5f9' }} {...p} />,
    blockquote: (p) => <blockquote style={{ margin: '0.35em 0', paddingLeft: 10, borderLeft: '2px solid #1e3a4f', color: '#94a3b8' }} {...p} />,
    hr: (p) => <hr style={{ border: 'none', borderTop: '1px solid #1e3a4f', margin: '0.5em 0' }} {...p} />,
    code: MdCode(noPaths),
    pre: MdPre,
  }
}

function MdCode(noPaths) {
  return function Code({ node, children, ...rest }) {
    const inPre = useContext(InPre)
    const text = typeof children === 'string' ? children : ''
    const fromText = rest['data-chat-path-text'] === 'true'
    if (!inPre && !noPaths && text && (fromText || codeLooksLikePath(text))) return <ChatPathChip token={text} fromText={fromText} />
    if (fromText) return <>{text}</>
    return <code style={codeStyle} {...rest}>{children}</code>
  }
}

// Renders an assistant/error message's Markdown. No rehype-raw plugin —
// react-markdown's default behavior already keeps embedded raw HTML as
// literal text, exactly what the plan requires ("raw HTML stays text").
// noPaths: file contents (the preview) name paths relative to themselves, so
// they are not looked up against the chat's folder.
const withPaths = makeComponents(false)
const withoutPaths = makeComponents(true)
const plugins = [remarkGfm, remarkChatPaths]
const pluginsNoPaths = [remarkGfm]

const plainStyle = { fontFamily: 'var(--font-mono)', fontSize: 11, lineHeight: 1.55, whiteSpace: 'pre-wrap', wordBreak: 'break-word', margin: 0 }

// Text that is shown as it is: too deeply nested to parse, or a render fault.
function PlainText({ content }) {
  return <pre data-testid="chat-markdown-plain" style={plainStyle}>{content}</pre>
}

export function ChatMarkdown({ content, noPaths = false }) {
  if (!content) return null
  if (markdownTooDeep(content)) return <PlainText content={content} />
  return (
    <ErrorBoundary key={content.length} fallback={() => <PlainText content={content} />}>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, lineHeight: 1.55, wordBreak: 'break-word' }}>
        <ReactMarkdown remarkPlugins={noPaths ? pluginsNoPaths : plugins} components={noPaths ? withoutPaths : withPaths}>{content}</ReactMarkdown>
      </div>
    </ErrorBoundary>
  )
}
