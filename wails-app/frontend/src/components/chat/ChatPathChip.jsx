import { useContext, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FolderOpen } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { WorkspaceImageContext } from './ChatAttachments.jsx'
import { ChatFilePreview } from './ChatFilePreview.jsx'
import { splitLine } from './chatLinks.js'

// A path a chat message names. It looks like a link only once the Go side
// says it exists (ResolveChatPath): the token is a model's guess until then.
// Click: preview a markdown/text file, otherwise show it in the file manager.
// Ctrl/Cmd-click (or the folder button) always reveals. Nothing here opens a
// file with its default handler, so nothing can run.

const MISSING = { kind: 'missing' }
const cache = new Map() // `${conversation}\0${token}` → { at, info }
const MISSING_TTL = 3000

function lookup(conversationId, token, force) {
  const key = `${conversationId}\0${token}`
  const hit = cache.get(key)
  if (!force && hit && (hit.info.kind !== 'missing' || Date.now() - hit.at < MISSING_TTL)) return Promise.resolve(hit.info)
  if (typeof api.resolveChatPath !== 'function') return Promise.resolve(MISSING)
  return Promise.resolve(api.resolveChatPath(conversationId, token)).then(
    (info) => { const v = info && info.kind ? info : MISSING; cache.set(key, { at: Date.now(), info: v }); return v },
    () => MISSING,
  )
}

export function clearChatPathCache() { cache.clear() }

const link = { background: 'none', border: 'none', padding: 0, cursor: 'pointer', color: '#00b4d8', textDecoration: 'underline', font: 'inherit' }
const codeStyle = { background: '#020509', padding: '1px 4px', borderRadius: 3 }

export function ChatPathChip({ token, fromText = false }) {
  const { t } = useTranslation()
  const conversationId = useContext(WorkspaceImageContext)
  const [info, setInfo] = useState(null)
  const [open, setOpen] = useState(false)

  useEffect(() => {
    let live = true
    lookup(conversationId, token).then((i) => { if (live) setInfo(i) })
    return () => { live = false }
  }, [conversationId, token])

  const recheck = () => { if (!info || info.kind === 'missing') lookup(conversationId, token, true).then(setInfo) }
  const reveal = () => Promise.resolve(api.revealChatPath(conversationId, token)).catch((e) => notify('reveal', t('chatLinks.revealFailed', { message: e?.message || String(e) })))

  if (!info || info.kind === 'missing') {
    return fromText
      ? <span onMouseEnter={recheck}>{token}</span>
      : <code onMouseEnter={recheck} style={codeStyle}>{token}</code>
  }
  const { line } = splitLine(token)
  const where = line ? ` (${t('chatLinks.line', { line })})` : ''
  if (info.kind === 'dir') {
    return <button type="button" style={link} onClick={reveal} title={`${t('chatLinks.openFolder')}: ${info.path}`}>{token}</button>
  }
  const previewable = !!info.previewable
  const label = token.split(/[\\/]/).pop()
  return (
    <span style={{ whiteSpace: 'nowrap' }}>
      <button type="button" style={link} title={`${previewable ? t('chatLinks.previewHint') : t('chatLinks.revealHint')}: ${info.path}${where}`}
        onClick={(e) => { if (!previewable || e.ctrlKey || e.metaKey) reveal(); else setOpen(true) }}>
        {token}
      </button>
      {previewable && (
        <button type="button" style={{ ...link, textDecoration: 'none', marginLeft: 3, verticalAlign: 'middle' }} onClick={reveal}
          aria-label={t('chatLinks.revealNamed', { name: label })} title={t('chatLinks.revealHint')}>
          <FolderOpen size={10} />
        </button>
      )}
      {open && <ChatFilePreview conversationId={conversationId} token={token} label={info.path} onReveal={reveal} onClose={() => setOpen(false)} />}
    </span>
  )
}
