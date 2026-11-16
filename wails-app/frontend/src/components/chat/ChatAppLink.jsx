import { createContext, useContext, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Workflow, Play, Building2, User, ListChecks, FileText, Settings, ArrowUpRight } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { tasksApi } from '../../services/tasks.js'
import { isUserGesture } from './chatLinks.js'
import { parseAppLink, needsLookup, resolveAppLink } from './chatAppLinks.js'

// The app's navigate(page, data), handed down by whoever renders the message.
// Without it an app link stays inert.
export const AppNavContext = createContext(null)

const ICONS = { workflow: Workflow, execution: Play, org: Building2, person: User, task: ListChecks, document: FileText, settings: Settings, page: ArrowUpRight }

const chip = {
  display: 'inline-flex', alignItems: 'center', gap: 3, background: 'rgba(0,180,216,0.1)',
  border: '1px solid rgba(0,180,216,0.3)', borderRadius: 4, padding: '0 5px', cursor: 'pointer',
  color: '#00b4d8', font: 'inherit', verticalAlign: 'baseline',
}
const inert = { color: '#94a3b8', textDecoration: 'underline dotted', cursor: 'default' }

// AppLink renders a monoagent:// link (chatAppLinks.js) as a chip with an
// icon once its target is confirmed real; until then, and for a target that
// is not real, it is the text alone (a mention) or the inert style (a link).
// budget is ChatMarkdown's per-message lookup budget; a link that finds none
// left stays inert, like a path.
export function AppLink({ href, children, mention = false, budget = null }) {
  const { t } = useTranslation()
  const onNavigate = useContext(AppNavContext)
  const link = parseAppLink(href)
  const lookup = !!link && needsLookup(link)
  const slot = useRef(null)
  if (lookup && onNavigate && slot.current === null) slot.current = !budget || budget.left-- > 0
  const [state, setState] = useState(link && !lookup && onNavigate ? 'ok' : 'pending')

  useEffect(() => {
    if (!link || !onNavigate) return undefined
    if (!lookup) { setState('ok'); return undefined }
    if (!slot.current) { setState('gone'); return undefined }
    let live = true
    setState('pending')
    resolveAppLink(link, { api, tasksApi }).then(r => { if (live) setState(r ? 'ok' : 'gone') })
    return () => { live = false }
  }, [href, !!onNavigate]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!link || !onNavigate || state !== 'ok') {
    if (mention || state === 'pending') return <>{children}</>
    return <span title={t('chatLinks.appUnknown')} style={inert}>{children}</span>
  }
  const Icon = ICONS[link.kind]
  const what = t(`chatLinks.appKind.${link.kind}`)
  const go = async (e) => {
    e.preventDefault()
    if (!isUserGesture(e)) return
    const target = await resolveAppLink(link, { api, tasksApi })
    if (!target) { setState('gone'); notify('chat', t('chatLinks.appGone', { what })); return }
    onNavigate(target.page, target.data)
  }
  return (
    <button type="button" data-chat-app-link={link.kind} style={chip} onClick={go}
      title={t('chatLinks.appOpen', { what })}>
      <Icon size={10} aria-hidden="true" />{children}
    </button>
  )
}
