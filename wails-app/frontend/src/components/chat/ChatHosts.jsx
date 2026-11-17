import { useContext, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { api, notify } from '../../services/api.js'
import { tasksApi } from '../../services/tasks.js'
import { isUserGesture } from './chatLinks.js'
import { AppNavContext } from './ChatAppLink.jsx'
import { parseAppLink, resolveAppLink } from './chatAppLinks.js'
import { ChatBlocksHost } from './blocks/context.js'
import { HtmlViewContext } from './htmlView.jsx'

// The one way a monoagent:// link opens from inside a message: the strict
// allowlist parse (parseAppLink), the backend lookup (resolveAppLink), then
// the app's navigate, in that order and nothing before the lookup resolves.
// `gesture` says a person really clicked: blocks pass isUserGesture(event);
// the html-view frame passes the host gate's verdict (a host-seen pointer,
// not a flag the frame can set). Without onNavigate or a gesture it is inert.
export function useAppLinkOpener() {
  const { t } = useTranslation()
  const onNavigate = useContext(AppNavContext)
  return useMemo(() => {
    if (!onNavigate) return null
    return async (href, gesture) => {
      if (!gesture) return
      const link = parseAppLink(href)
      if (!link) return
      const target = await resolveAppLink(link, { api, tasksApi })
      if (!target) { notify('chat', t('chatLinks.appGone', { what: t(`chatLinks.appKind.${link.kind}`) })); return }
      onNavigate(target.page, target.data)
    }
  }, [onNavigate, t])
}

// Fills in what only the message renderer knows: how to open an app link.
// Blocks get onAppLink (event-aware); html-view gets onSend (the host's, which
// the frame block confirms with the user before calling) and a gate-vouched
// onAppLink. A host that supplies its own onAppLink keeps it.
export function ChatHosts({ children }) {
  const open = useAppLinkOpener()
  const host = useContext(ChatBlocksHost)
  const blocks = useMemo(() => (open ? { ...host, onAppLink: host?.onAppLink || ((href, e) => open(href, isUserGesture(e))) } : host), [host, open])
  const frame = useMemo(() => ({ onSend: host?.onSend, onAppLink: open ? (href) => open(href, true) : undefined }), [host?.onSend, open])
  return (
    <ChatBlocksHost.Provider value={blocks}>
      <HtmlViewContext.Provider value={frame}>{children}</HtmlViewContext.Provider>
    </ChatBlocksHost.Provider>
  )
}
