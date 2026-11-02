import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, Wrench } from 'lucide-react'
import { ChatMarkdown } from '../chat/ChatMarkdown.jsx'
import { ChatComposer } from '../chat/ChatComposer.jsx'
import { threadView, toolSummary } from '../../lib/orgBubble.js'
import { normalizeAsk } from '../../lib/askModel.js'
import { AskCard } from './AskCard.jsx'
import { ToolLink } from '../chat/ToolLink.jsx'
import { cliOf, cliTitle, vaultRelatedCall } from '../chat/toolLinks.js'
import { useHilAsks } from './useHilAsks.js'
import '../chat/chat.css'

const mono = 'var(--font-mono)'
const AMBER = '#fbbf24'

function Muted({ children, style }) {
  return <div style={{ fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', ...style }}>{children}</div>
}

// TeamRow is a role-to-role message: one compact line that opens to the
// text, so the boss's conversation stays readable. In the Team view every
// row starts open: that view is the conversation.
function TeamRow({ item, nameOf, defaultOpen = false }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(defaultOpen)
  const from = item.external ? item.from : nameOf(item.from)
  const to = item.external ? item.to : nameOf(item.to)
  return (
    <div data-testid="org-chat-team" style={{ margin: '2px 0' }}>
      <button type="button" onClick={() => setOpen(o => !o)} aria-expanded={open}
        style={{ display: 'flex', alignItems: 'center', gap: 5, width: '100%', textAlign: 'left', background: 'transparent', border: 'none', padding: '2px 0', cursor: 'pointer', fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)' }}>
        <ChevronRight size={10} style={{ transform: open ? 'rotate(90deg)' : 'none', transition: 'transform 120ms' }} aria-hidden="true" />
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {t('orgBubble.teamRow', { from, to })}{item.subject ? ` · ${item.subject}` : ''}
        </span>
      </button>
      {open && <div style={{ padding: '2px 0 4px 15px', fontSize: 11 }}><ChatMarkdown content={item.text || ''} /></div>}
    </div>
  )
}

// ToolRow is one tool call of a role, in that role's thread.
function ToolRow({ item, onNavigate }) {
  const { t } = useTranslation()
  const call = item.call
  // A monoagentcli call on a tab links to it; a vault call shows the entry's
  // name only, and one it cannot read a neutral label: never its command
  // line (#473, #872).
  const cli = cliOf(call)
  const label = cli?.family === 'vault' ? cliTitle(cli, t) : vaultRelatedCall(call) ? t('toolLinks.vaultRelated') : toolSummary(call)
  const state = call.status === 'started' ? 'running' : call.ok === false ? (call.denied ? 'denied' : 'failed') : 'ok'
  return (
    <div data-testid="org-chat-tool" data-state={state} className="org-tool-row">
      <Wrench size={10} aria-hidden="true" />
      <span className="org-tool-name">{label}</span>
      <span className="org-tool-state" data-state={state}>{t(`orgBubble.tool.${state}`)}</span>
      <ToolLink link={cli && (cli.family === 'vault' || !vaultRelatedCall(call)) && { page: cli.page }} onNavigate={onNavigate} />
    </div>
  )
}

function Speech({ who, mine, children, note }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: mine ? 'flex-end' : 'flex-start', margin: '6px 0' }}>
      <Muted style={{ marginBottom: 2 }}>{who}</Muted>
      <div style={{
        maxWidth: '85%', padding: '7px 10px', borderRadius: 10, fontSize: 12, lineHeight: 1.5,
        background: mine ? 'rgba(0,180,216,0.12)' : 'var(--elevated)', border: '1px solid var(--border)', color: 'var(--text)',
      }}>
        {children}
      </div>
      {note && <Muted style={{ marginTop: 2 }}>{note}</Muted>}
    </div>
  )
}

// OrgChatView is the chat half of an expanded org bubble (#229): the boss
// thread from `org chat history`, or one role's slice of it, or the team's
// role-to-role timeline (view); every ask answered inline by one AskCard
// (questions, approvals and gates through the org chat, workflow HIL items
// and sends through the HIL queue); and a composer that messages the boss.
//   conv     useOrgBubble()      nameOf  role id → name
//   view     { mode: 'boss' | 'team' | 'role', role }
//   onView   switch the view (the "waiting elsewhere" hint goes to the boss)
export function OrgChatView({ conv, nameOf, draft, onDraftChange, orgName = '', view = { mode: 'boss' }, onView, onNavigate }) {
  const { t } = useTranslation()
  const scrollRef = useRef(null)
  const { history, historyError, items, echoes, running, sending, sendError, busy, outcome, readOnly } = conv
  const canAct = running
  const roleIds = useMemo(() => (history?.roles || []).map(r => r.id), [history])
  const hil = useHilAsks({ org: orgName, roleIds }, !!history && !readOnly)

  const calls = conv.bubble?.calls
  const callIds = conv.bubble?.callIds
  const shown = useMemo(() => threadView(items, view, { calls, callIds }), [items, view, calls, callIds])

  // Asks outside the current view must not hide: say how many wait.
  const inView = ask => view.mode === 'boss' || (view.mode === 'role' && ask.role === view.role)
  const hilIds = new Set(hil.asks.map(a => a.id))
  const historySends = shown.filter(it => it.kind === 'send' && !hilIds.has(it.id))
  const shownAsks = hil.asks.filter(inView)
  const pendingHidden =
    items.filter(it => it.pending && !shown.includes(it)).length +
    hil.asks.filter(a => a.pending && !inView(a)).length

  // Stick to the bottom as the thread grows, unless the person scrolled up.
  const atBottom = useRef(true)
  useEffect(() => {
    const el = scrollRef.current
    if (el && atBottom.current) el.scrollTop = el.scrollHeight
  }, [shown, echoes, hil.asks])
  // A different view starts at its end.
  useEffect(() => { atBottom.current = true }, [view.mode, view.role])

  const onSend = async (sentText) => {
    if (await conv.send(typeof sentText === 'string' ? sentText : draft)) onDraftChange('')
  }
  const answer = (ask, text) => conv.resolveItem(ask.ref, { answer: text })
  const dismiss = ask => conv.resolveItem(ask.ref, { dismiss: true })
  const decide = (ask, approve, extras = {}) => (ask.kind === 'hil' || ask.kind === 'send'
    ? hil.resolve(ask, approve, extras)
    : conv.resolveItem(ask.ref, { approve, note: extras.note || '' }))

  const bossName = history?.boss_title || history?.boss || t('orgBubble.boss')
  let composerNote = ''
  if (history && readOnly) composerNote = t('orgBubble.finishedNote')
  else if (history && !running) composerNote = t('orgBubble.queuedNote')
  const threadLabel = view.mode === 'team' ? t('orgBubble.threadTeam') : view.mode === 'role' ? t('orgBubble.threadRoleLabel', { name: nameOf(view.role) }) : t('orgBubble.threadBoss')
  const nothing = history && shown.length === 0 && shownAsks.length === 0 && historySends.length === 0 && (view.mode !== 'boss' || echoes.length === 0)

  const card = (ask, key) => {
    const isHil = ask.kind === 'hil' || ask.kind === 'send'
    return (
      <AskCard key={key} ask={ask} nameOf={nameOf}
        canAct={readOnly ? false : isHil ? true : canAct} readOnly={!!readOnly}
        busy={isHil ? !!hil.busy[ask.id] : !!busy[ask.ref]}
        outcome={isHil ? hil.outcome[ask.id] : outcome[ask.ref]}
        onAnswer={answer} onDismiss={dismiss} onDecide={decide} onNavigate={onNavigate} />
    )
  }

  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
      <div ref={scrollRef} data-testid="org-chat-thread" role="log" aria-live="polite" aria-relevant="additions" aria-label={threadLabel}
        aria-busy={!history && !historyError}
        tabIndex={0}
        onScroll={e => { const el = e.currentTarget; atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40 }}
        style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: '8px 14px' }}>
        {!history && !historyError && <Muted style={{ padding: 8 }}>{t('bubbles.loadingChat')}</Muted>}
        {historyError && !history && <div role="alert" style={{ padding: 8, fontFamily: mono, fontSize: 10, color: 'var(--red, #ef4444)' }}>{t('orgBubble.couldNotLoad', { error: historyError })}</div>}
        {history && view.mode === 'boss' && items.length === 0 && echoes.length === 0 && shownAsks.length === 0 && <Muted style={{ padding: 8 }}>{t('orgBubble.empty', { boss: bossName })}</Muted>}
        {nothing && view.mode === 'team' && <Muted style={{ padding: 8 }} >{t('orgBubble.teamEmpty')}</Muted>}
        {nothing && view.mode === 'role' && <Muted style={{ padding: 8 }}>{t('orgBubble.roleEmpty', { name: nameOf(view.role) })}</Muted>}
        {history && !running && !readOnly && items.some(it => it.pending) && (
          <div data-testid="org-chat-stopped" style={{ margin: '4px 0 8px', padding: '6px 10px', borderRadius: 'var(--radius)', border: '1px solid rgba(251,191,36,0.3)', fontFamily: mono, fontSize: 10, color: AMBER }}>
            {t('orgBubble.notRunning')}
          </div>
        )}
        {pendingHidden > 0 && (
          <button type="button" className="org-waiting-hint" data-testid="org-chat-waiting-elsewhere" onClick={() => onView?.({ mode: 'boss' })}>
            {t('orgBubble.waitingElsewhere', { count: pendingHidden })}
          </button>
        )}
        {shown.map(item => {
          const key = `${item.kind}-${item.id || item.ref}`
          switch (item.kind) {
            case 'human':
              return <Speech key={key} who={t('orgBubble.you')} mine>{item.text}</Speech>
            case 'boss':
              return <Speech key={key} who={nameOf(item.role)}><ChatMarkdown content={item.text || ''} /></Speech>
            case 'question':
            case 'approval':
            case 'gate':
              return card(item, key)
            case 'send': {
              const ask = normalizeAsk(item)
              return ask ? card(ask, key) : null
            }
            case 'team':
              return <TeamRow key={`${view.mode}-${key}`} item={item} nameOf={nameOf} defaultOpen={view.mode === 'team'} />
            case 'tool':
              return <ToolRow key={key} item={item} onNavigate={onNavigate} />
            case 'status':
              return view.mode === 'boss'
                ? <Muted key={key} style={{ textAlign: 'center', margin: '6px 0' }}>{/^org started/.test(item.text) ? t('orgBubble.started') : t('orgBubble.stopped')}</Muted>
                : null
            default:
              return null
          }
        })}
        {shownAsks.map(ask => card(ask, `ask-${ask.id}`))}
        {view.mode === 'boss' && echoes.map(e => (
          <Speech key={e.id} who={t('orgBubble.you')} mine note={e.delivery === 'queued' ? t('orgBubble.queued') : t('orgBubble.sent')}>{e.text}</Speech>
        ))}
      </div>
      {sendError && <div role="alert" style={{ padding: '0 14px 4px', fontFamily: mono, fontSize: 10, color: 'var(--red, #ef4444)' }}>{t('orgBubble.couldNotSend', { error: sendError })}</div>}
      {composerNote && <Muted style={{ padding: '0 14px 2px' }}>{composerNote}</Muted>}
      <ChatComposer value={draft} onChange={onDraftChange} onSend={onSend} streaming={false}
        disabled={sending || !history || readOnly} disabledReason="" />
    </div>
  )
}
