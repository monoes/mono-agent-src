import { useContext, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FolderOpen } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { WorkspaceImageContext } from './ChatAttachments.jsx'
import { ChatFilePreview } from './ChatFilePreview.jsx'
import { splitLine } from './chatLinks.js'

// A path a chat message names. At render it is only a neutral "path" chip:
// ClassifyChatPath judges its spelling and makes no filesystem call, because
// resolving a model-chosen path can touch a network share (a symlink or
// junction in the chat folder may lead there). The disk is consulted when
// the user clicks (or presses Enter/Space on) it, never on hover or focus:
// even a path spelled inside the chat folder may run through a link to a
// network share, and a lookup cannot be undone by refusing its result. The
// first activation resolves and then previews (a markdown/text file) or shows
// it in the file manager in one go; a path that does not exist reads 'Not
// found' afterwards. Hover and focus only show a title. Ctrl/Cmd-click (or the folder button) always reveals. Nothing
// here opens a file with its default handler, so nothing can run.

const MISSING = { kind: 'missing' }
const cache = new Map() // `${conversation}\0${token}` → { at, info }
const MISSING_TTL = 3000
const MAX_CACHE = 500

function lookup(conversationId, token, force) {
  const key = `${conversationId}\0${token}`
  const hit = cache.get(key)
  if (!force && hit && (hit.info.kind !== 'missing' || Date.now() - hit.at < MISSING_TTL)) return Promise.resolve(hit.info)
  if (typeof api.resolveChatPath !== 'function') return Promise.resolve(MISSING)
  return Promise.resolve(api.resolveChatPath(conversationId, token)).then(
    (info) => { const v = info && info.kind ? info : MISSING; cache.delete(key); cache.set(key, { at: Date.now(), info: v }); if (cache.size > MAX_CACHE) cache.delete(cache.keys().next().value); return v },
    () => MISSING,
  )
}

export function clearChatPathCache() { cache.clear() }
export function chatPathCacheSize() { return cache.size }

const link = { background: 'none', border: 'none', padding: 0, cursor: 'pointer', color: '#00b4d8', textDecoration: 'underline', font: 'inherit' }
const codeStyle = { background: '#020509', padding: '1px 4px', borderRadius: 3 }
// Not yet checked: looks like code, with a dotted underline as the only hint.
const unchecked = { ...codeStyle, border: 'none', cursor: 'pointer', color: 'inherit', font: 'inherit', textDecoration: 'underline dotted', textDecorationColor: '#64748b' }

export function ChatPathChip({ token, fromText = false }) {
  const { t } = useTranslation()
  const conversationId = useContext(WorkspaceImageContext)
  const [cls, setCls] = useState(null) // 'inside' | 'outside' | 'refused' | null while unknown
  const [info, setInfo] = useState(null)
  const [open, setOpen] = useState(false)

  useEffect(() => {
    let live = true
    setCls(null); setInfo(null)
    const ask = typeof api.classifyChatPath === 'function' ? api.classifyChatPath(conversationId, token) : 'refused'
    Promise.resolve(ask).then((c) => { if (!live) return; const k = typeof c === 'string' ? c : c?.class; setCls(k === 'inside' || k === 'outside' ? k : 'refused') }, () => { if (live) setCls('refused') })
    return () => { live = false }
  }, [conversationId, token])

  const reveal = () => Promise.resolve(api.revealChatPath(conversationId, token)).catch((e) => notify('reveal', t('chatLinks.revealFailed', { message: e?.message || String(e) })))
  const check = () => lookup(conversationId, token, !!info).then((i) => { setInfo(i); return i })
  const clickUnchecked = (e) => {
    check().then((i) => {
      if (i.kind === 'missing') return
      if (i.kind === 'file' && i.previewable && !e.ctrlKey && !e.metaKey) setOpen(true)
      else reveal()
    })
  }

  if (!cls || cls === 'refused') return fromText ? <>{token}</> : <code style={codeStyle}>{token}</code>
  if (!info || info.kind === 'missing') {
    return (
      <button type="button" data-chat-path="unchecked" style={unchecked} onClick={clickUnchecked}
        title={info ? t('chatLinks.notFound') : t('chatLinks.checkHint')}>
        {token}
      </button>
    )
  }
  const { line } = splitLine(token)
  const where = line ? ` (${t('chatLinks.line', { line })})` : ''
  if (info.kind === 'dir') {
    return <button type="button" style={link} onClick={reveal} title={`${t('chatLinks.openFolder')}: ${info.path || token}`}>{token}</button>
  }
  const previewable = !!info.previewable
  const label = token.split(/[\\/]/).pop()
  return (
    <span style={{ whiteSpace: 'nowrap' }}>
      <button type="button" style={link} title={`${previewable ? t('chatLinks.previewHint') : t('chatLinks.revealHint')}: ${info.path || token}${where}`}
        onClick={(e) => { if (!previewable || e.ctrlKey || e.metaKey) reveal(); else setOpen(true) }}>
        {token}
      </button>
      {previewable && (
        <button type="button" style={{ ...link, textDecoration: 'none', marginLeft: 3, verticalAlign: 'middle' }} onClick={reveal}
          aria-label={t('chatLinks.revealNamed', { name: label })} title={t('chatLinks.revealHint')}>
          <FolderOpen size={10} />
        </button>
      )}
      {open && <ChatFilePreview conversationId={conversationId} token={token} label={info.path || token} onReveal={reveal} onClose={() => setOpen(false)} />}
    </span>
  )
}
