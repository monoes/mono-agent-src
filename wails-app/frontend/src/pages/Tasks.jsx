import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AppWindow, Archive, Plus, Check, CheckCheck, CircleHelp, Globe, LayoutGrid, NotebookText, Sparkles, Terminal, Undo2 } from 'lucide-react'
import { tasksApi } from '../services/tasks.js'
import { useTaskBoard } from '../lib/useTaskBoard.js'
import { COLUMNS, findTask, filterBoard, placeFor, isNoopDrop, keyMove, focusTarget, isTypingTarget, claimState, sourceChip, shortAge, splitMinutes, actorColor, readFirst } from '../lib/taskModel.js'
import TaskDrawer from '../components/TaskDrawer.jsx'
import { useFlip } from '../lib/useFlip.js'
import { useCardDrag } from '../lib/useCardDrag.js'
import { useMarks } from '../lib/useMarks.js'
import { usePageVisible } from '../lib/usePageVisible.js'
import { boardEditable } from '../lib/taskSessionModel.js'
import CardBadges from '../components/tasksessions/CardBadges.jsx'
import TasksShell from '../components/tasksessions/TasksShell.jsx'

const ARROWS = { ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' }

function QuickAdd({ status, onAdd, t }) {
  const [title, setTitle] = useState('')
  const submit = (e) => {
    e.preventDefault()
    const v = title.trim()
    if (!v) return
    onAdd({ title: v, ...(status === 'ready' ? { ready: true } : {}) })
    setTitle('')
  }
  return (
    <form onSubmit={submit} className="tb-quick">
      <input className="form-input" aria-label={t('tasks.quickAddPlaceholder')} placeholder={t('tasks.quickAddPlaceholder')} value={title} onChange={e => setTitle(e.target.value)} />
      <button className="btn btn-secondary btn-sm" type="submit" disabled={!title.trim()}>{status === 'ready' ? t('tasks.addToReady') : t('tasks.add')}</button>
    </form>
  )
}

const CAPTURE_COMMANDS = ['monoagentcli task add -- "Title"', 'monoagentcli task add --stdin < notes.txt']

function CaptureHelp({ t }) {
  return (
    <details className="tb-help">
      <summary><CircleHelp size={12} /> {t('tasks.capture.title')}</summary>
      <ul>
        <li>{t('tasks.capture.chrome')}</li>
        <li>{t('tasks.capture.mac')}</li>
        <li>{t('tasks.capture.cli')} {CAPTURE_COMMANDS.map(c => <code key={c}>{c}</code>)}</li>
        <li>{t('tasks.capture.agents')}</li>
      </ul>
    </details>
  )
}

const SOURCE_ICONS = { chrome: Globe, os: AppWindow, agent: Sparkles, app: LayoutGrid, cli: Terminal }

function span(minutes, t) {
  const { h, m } = splitMinutes(minutes)
  return h ? t('tasks.span.hm', { h, m }) : t('tasks.span.m', { m })
}

// The card is a plain container: the title is its one control that opens it
// (and takes the move keys), and the approve and archive buttons are siblings
// of it, so no control sits inside another. A card with notes shows a notes
// marker, and its Inbox button reads "Read and approve" (plan ruling F6). A
// Review card has Done and Back to Ready. A press on it can start the ghost
// drag (useCardDrag); while it is dragged it stays as a faded placeholder.
function Card({ task, status, readOnly, selected, dragging, pulse, now, badges, onOpen, onKey, onPress, onApprove, onMove, onArchive, t }) {
  const chip = sourceChip(task)
  const Icon = SOURCE_ICONS[chip.kind] || Terminal
  const age = shortAge(task.created_at, now)
  const cs = claimState(task.claim, now)
  const tag = status === 'review' && (task.last_event?.kind === 'question' || task.last_event?.kind === 'result') ? task.last_event.kind : null
  const notes = readFirst(task)
  const meta = { fontSize: 11, opacity: 0.8, display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap', fontFamily: 'var(--mono, monospace)' }
  return (
    <div
      data-task-id={task.id}
      onMouseDown={e => onPress(e, task.id, status)}
      onClick={e => { if (!e.target.closest('button')) onOpen(task.id) }}
      className={`tb-card${selected ? ' is-selected' : ''}${dragging ? ' tb-card--dragging' : ''}${cs?.stale ? ' is-stale' : ''}${pulse ? ` tb-card--${pulse}` : ''}`}
    >
      <button
        type="button"
        data-card-open
        aria-pressed={selected}
        onClick={() => onOpen(task.id)}
        onKeyDown={e => onKey(e, task)}
        className="tb-card-title"
      >{task.title}</button>
      <div style={{ ...meta, marginTop: 4 }}>
        <span className="tb-chip" title={chip.text ? `${t(`tasks.source.${chip.kind}`)}: ${chip.text}` : t(`tasks.source.${chip.kind}`)}>
          <Icon size={11} aria-hidden="true" /><span className="tb-chip-text">{chip.text || t(`tasks.source.${chip.kind}`)}</span>
        </span>
        {task.created_at && <span data-testid="age">{t(`tasks.age.${age.unit}`, { n: age.n })}</span>}
        <span>#{task.id}</span>
        {notes && <span data-testid="notes-marker" title={t('tasks.hasNotes')} style={{ display: 'inline-flex', gap: 3, alignItems: 'center' }}><NotebookText size={11} aria-hidden="true" />{t('tasks.notesMarker')}</span>}
        {tag && <span data-testid="tag" style={{ color: tag === 'question' ? 'var(--orange, #f59e0b)' : 'var(--green, #10b981)' }}>{t(`tasks.tag.${tag}`)}</span>}
      </div>
      {badges}
      {cs && (
        <div style={{ ...meta, marginTop: 4 }} title={cs.stale ? t('tasks.leaseEnded') : undefined}>
          <span aria-hidden="true" style={{ width: 16, height: 16, borderRadius: '50%', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', fontSize: 9, border: `1.5px ${cs.stale ? 'dashed var(--orange, #f59e0b)' : `solid ${actorColor(cs.by)}`}` }}>{cs.initial}</span>
          <span>{cs.stale ? t('tasks.stale') : t('tasks.claimedBy', { by: cs.by })}</span>
          <span data-testid="lease">{cs.stale ? t('tasks.ended', { time: span(cs.minutes, t) }) : t('tasks.left', { time: span(cs.minutes, t) })}</span>
        </div>
      )}
      {!readOnly && (
        <div className="tb-card-actions">
          {status === 'inbox' && (
            <button className="btn btn-ghost btn-icon btn-sm" type="button" title={notes ? t('tasks.readAndApprove') : t('tasks.approve')}
              aria-label={`${notes ? t('tasks.readAndApprove') : t('tasks.approve')}: ${task.title}`} onClick={() => onApprove(task)}><Check size={12} /></button>
          )}
          {status === 'review' && (
            <>
              <button className="btn btn-ghost btn-sm tb-done" type="button" aria-label={`${t('tasks.markDone')}: ${task.title}`} onClick={() => onMove(task.id, 'done')}><CheckCheck size={12} /> {t('tasks.markDone')}</button>
              <button className="btn btn-ghost btn-sm" type="button" aria-label={`${t('tasks.backToReady')}: ${task.title}`} onClick={() => onMove(task.id, 'ready')}><Undo2 size={12} /> {t('tasks.backToReady')}</button>
            </>
          )}
          <button className="btn btn-ghost btn-icon btn-sm" type="button" title={t('tasks.archive')} aria-label={`${t('tasks.archive')}: ${task.title}`} onClick={() => onArchive(task.id)}><Archive size={12} /></button>
        </div>
      )}
    </div>
  )
}

// The board page of Personal (no session) or of one task session: session is
// the CLI's session document, and the rest are what TasksShell puts around it
// (the switcher, the session header, the plan view in place of the columns).
export function TaskBoard({ isActive = true, switcher = null, session = null, sessionEvents = [], sessionHeader = null, renderPlan = null, openTaskId = null }) {
  const { t } = useTranslation()
  const b = useTaskBoard(isActive, session?.id ?? null)
  const [query, setQuery] = useState('')
  const [openId, setOpenId] = useState(null)
  // readFor: the card the drawer opened for to be read before it leaves
  // Inbox (F6), and the move it was asked for: {id, to, place}.
  const [readFor, setReadFor] = useState(null)
  const [shell, setShell] = useState(null) // null until agentShell() answers
  const root = useRef(null)
  const searchRef = useRef(null)
  // What the live region last said: each move is announced (spec §10).
  const [announcement, setAnnouncement] = useState('')
  const refocus = useRef(null) // a card moved by key: a move to another column remounts it

  useFlip(root)
  const marks = useMarks(b.board)
  // A lease ends with no write, so no read follows: the clock decides.
  const [now, setNow] = useState(() => Date.now())
  const visible = usePageVisible()
  useEffect(() => {
    if (!isActive || !visible) return undefined // a hidden window does not tick
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 30000)
    return () => clearInterval(timer)
  }, [isActive, visible])
  useEffect(() => { tasksApi.agentShell().then(v => setShell(v || '')) }, [])
  const agentLocked = shell === null || !!shell
  // Only a running session's board takes moves; the others are read until the operator acts in the header.
  const readOnly = agentLocked || !boardEditable(session)
  const shown = useMemo(() => filterBoard(b.board, query), [b.board, query])
  const [archiving, setArchiving] = useState(null)
  const [archiveError, setArchiveError] = useState('')
  const lastOpen = useRef(null)
  const found = openId && b.board ? findTask(b.board, openId)?.task : null
  if (found) lastOpen.current = found
  const open = found || (archiving && archiving === openId ? lastOpen.current : null)
  useEffect(() => { setArchiveError('') }, [openId])

  // No dependency list: any render may have remounted the card the keys moved.
  useLayoutEffect(() => {
    const id = refocus.current
    if (!id) return
    const el = root.current?.querySelector(`[data-task-id="${id}"] [data-card-open]`)
    if (!el) return
    refocus.current = null
    if (document.activeElement !== el) el.focus()
  })

  useEffect(() => {
    if (!openId) return undefined
    // Escape in a field only leaves it (an edit in progress is not lost); elsewhere it closes the drawer.
    const onEsc = (e) => {
      if (e.key !== 'Escape') return
      if (isTypingTarget(e.target)) e.target.blur()
      else setOpenId(null)
    }
    window.addEventListener('keydown', onEsc)
    return () => window.removeEventListener('keydown', onEsc)
  }, [openId])

  const focusCard = (id) => root.current?.querySelector(`[data-task-id="${id}"] [data-card-open]`)?.focus()
  // A move is announced once the CLI has answered: done, or refused and why.
  const announced = async (id, to, call) => {
    const title = findTask(b.board, id)?.task.title || `#${id}`
    const res = await call()
    const column = t(`tasks.column.${to}`)
    setAnnouncement(res?.error ? t('tasks.announce.failed', { title, column, error: res.error }) : t('tasks.announce.moved', { title, column }))
    return res
  }
  const moveCard = (id, to, place) => announced(id, to, () => b.move(id, to, place))
  const approveNow = (id) => announced(id, 'ready', () => b.approve(id))
  const openQuickAdd = () => root.current?.querySelector('.tb-quick input')?.focus()

  // `/` focuses search and `N` the Inbox quick add, from anywhere on the page
  // but a field (spec §10, Keyboard); only while this page is the one shown.
  useEffect(() => {
    if (!isActive) return undefined
    const onKeyDown = (e) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || isTypingTarget(e.target)) return
      if (e.key === '/') { e.preventDefault(); searchRef.current?.focus() }
      else if ((e.key === 'n' || e.key === 'N') && !readOnly) { e.preventDefault(); openQuickAdd() }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [isActive, readOnly])

  // A card with notes never leaves Inbox in one gesture (plan ruling F6, and
  // the #888 review for the other columns): Approve, Shift+Right and a drop
  // on any column open its drawer, where the person approves or moves it
  // with the notes in front of them.
  const openCard = (id) => { setReadFor(null); setOpenId(id) }
  // A task another page asks for (the report link of a chat) opens once its board has loaded.
  const askedFor = useRef(null)
  useEffect(() => {
    if (!openTaskId || !b.board || askedFor.current === openTaskId) return
    if (findTask(b.board, openTaskId)) { askedFor.current = openTaskId; setReadFor(null); setOpenId(openTaskId) }
  }, [openTaskId, b.board])
  const openToRead = (id, to = 'ready', place = { where: '' }) => { setOpenId(id); setReadFor({ id, to, place }) }
  const guarded = (task, from, to) => from === 'inbox' && to !== 'inbox' && readFirst(task)
  const approveCard = (task) => {
    if (guarded(task, 'inbox', 'ready')) openToRead(task.id)
    else approveNow(task.id)
  }

  // Archive closes the drawer only once the CLI has said yes. While it works the
  // drawer stays on the card as last shown (the optimistic removal takes the
  // card off the board); a refusal brings the card back and the drawer says why.
  const archiveRes = async (id) => {
    const inDrawer = openId === id
    if (inDrawer) { setArchiving(id); setArchiveError('') }
    const res = await b.archive(id)
    setArchiving(null)
    if (res?.error) { if (inDrawer) setArchiveError(res.error) } else setOpenId(cur => (cur === id ? null : cur))
    return res
  }

  const onKey = (e, task) => {
    if (e.target !== e.currentTarget) return
    const key = ARROWS[e.key]
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openCard(task.id); return }
    // `A` approves an Inbox card (a card with notes opens to be read first).
    if ((e.key === 'a' || e.key === 'A') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const at = findTask(shown, task.id)
      if (!readOnly && at?.status === 'inbox') { e.preventDefault(); refocus.current = task.id; approveCard(at.task) }
      return
    }
    if (!key) return
    e.preventDefault()
    if (e.shiftKey || e.altKey) {
      if (readOnly) return
      const m = keyMove(shown, task.id, key)
      if (m && !m.blocked && guarded(task, findTask(shown, task.id)?.status, m.to)) { openToRead(task.id, m.to, m.place); return }
      if (m && !m.blocked) { refocus.current = task.id; moveCard(task.id, m.to, m.place) }
      return
    }
    const next = focusTarget(shown, task.id, key)
    if (next) focusCard(next)
  }

  const onDrop = ({ id, from, fromIds, to, ids, index }) => {
    const found = findTask(shown, id)
    if (!found || readOnly || isNoopDrop(fromIds, from, to, ids, index, id)) return
    if (guarded(found.task, from, to)) { openToRead(id, to, placeFor(ids, index)); return }
    moveCard(id, to, placeFor(ids, index))
  }
  const { drag, onCardMouseDown, consumeClick } = useCardDrag({ rootRef: root, onDrop, canDrag: () => !readOnly, layoutKey: shown })
  const dragged = drag && findTask(shown, drag.id)?.task

  // Personal reads as it always did while it loads. A session keeps its page (and so the switcher and the
  // header, with whatever a person has typed in them) from the first paint: a board that is slow to load
  // or fails to read never takes them away, and they are not remounted when it arrives.
  if (!b.board && !session) {
    return <div className="page" style={{ padding: 16 }}>{switcher}{b.error ? <div role="alert">{t('tasks.loadError')}: {b.error}</div> : t('tasks.loading')}</div>
  }

  return (
    <div className={visible ? 'page tb-page' : 'page tb-page tb-paused'} ref={root}>
      <div className="page-header">
        <div className="page-header-left">
          <h1 className="page-title">{t('tasks.title')}</h1>
          {b.board?.profile?.name && <span className="tb-profile-pill" data-testid="tb-profile">{t('tasks.inProfile', { name: b.board.profile.name })}</span>}
        </div>
        <div className="page-header-right">
          <span className="tb-search">
            <input ref={searchRef} className="search-input" type="search" aria-label={t('tasks.search')} aria-keyshortcuts="/" placeholder={t('tasks.search')} value={query} onChange={e => setQuery(e.target.value)} />
            <kbd className="tb-kbd" aria-hidden="true">/</kbd>
          </span>
          {!readOnly && !session && <button type="button" className="btn btn-primary btn-sm" aria-keyshortcuts="N" onClick={openQuickAdd}><Plus size={12} /> {t('tasks.newTask')} <kbd className="tb-kbd">N</kbd></button>}
        </div>
      </div>
      <div role="status" aria-live="polite" data-testid="tb-live" className="tb-live">{announcement}</div>
      <div className="tb-body">
      {switcher}
      {sessionHeader}
      {!session && <CaptureHelp t={t} />}
      {agentLocked && <div role="note">{t('tasks.readOnly')}</div>}
      {b.error && <div role="alert">{t('tasks.loadError')}: {b.error}</div>}
      {b.notice && (
        <div role="alert" onClick={b.clearNotice}>{t('tasks.refused', { error: b.notice.error })}</div>
      )}
      <div className="tb-main">
        {!b.board ? (b.error ? null : <div className="tb-cols">{t('tasks.loading')}</div>) : renderPlan ? <div className="tb-cols">{renderPlan({ openTask: openCard })}</div> : <div className="tb-cols" data-tb-board-scroll>
          {COLUMNS.map(status => {
            const over = drag?.overStatus === status
            const lineAt = over ? (shown.columns[status].filter(x => x.id !== drag.id)[drag.overIndex]?.id ?? 'end') : null
            return (
              <section
                key={status}
                aria-label={t(`tasks.column.${status}`)}
                data-tb-column={status}
                data-drop-target={over ? 'true' : undefined}
                className="tb-col"
              >
                <h2 className="tb-col-head">{t(`tasks.column.${status}`)} <span className="tb-count">{shown.counts[status] ?? shown.columns[status].length}</span></h2>
                {!readOnly && !session && (status === 'inbox' || status === 'ready') && <QuickAdd status={status} onAdd={b.add} t={t} />}
                {shown.columns[status].length === 0 && <div className="tb-empty">{session ? t('tasks.empty') : t(`tasks.emptyBy.${status}`, { defaultValue: t('tasks.empty') })}</div>}
                {shown.columns[status].map(task => (
                  <Fragment key={task.id}>
                    {lineAt === task.id && <div className="tb-drop-line" data-testid="tb-drop-line" aria-hidden="true" />}
                    <Card
                      task={task} status={status} readOnly={readOnly} selected={openId === task.id} badges={session ? <CardBadges task={task} board={b.board} /> : null} dragging={drag?.id === task.id} now={now}
                      pulse={status === 'done' && marks.done.has(task.id) ? 'done-pulse' : status === 'in_progress' && task.claim && marks.claimed.has(task.id) ? 'claim-pulse' : ''}
                      onOpen={id => { if (!consumeClick()) openCard(id) }} onKey={onKey} onPress={onCardMouseDown}
                      onApprove={approveCard} onMove={(id, to) => { refocus.current = id; moveCard(id, to, { where: '' }) }} onArchive={archiveRes} t={t}
                    />
                  </Fragment>
                ))}
                {lineAt === 'end' && <div className="tb-drop-line" data-testid="tb-drop-line" aria-hidden="true" />}
              </section>
            )
          })}
        </div>}
        {open && <TaskDrawer task={open} focusArtifacts={open.id === openTaskId} sessionEvents={sessionEvents} status={findTask(b.board, open.id)?.status} readFirst={readFor?.id === open.id} readOnly={readOnly}
          moveTo={readFor?.id === open.id && readFor.to !== 'ready' ? readFor.to : null} onMoveTo={() => moveCard(open.id, readFor.to, readFor.place)}
          onClose={() => setOpenId(null)} onEdit={b.edit} onComment={b.comment} onApprove={() => approveNow(open.id)} onArchive={() => archiveRes(open.id)} archiving={archiving === open.id} archiveError={archiveError} />}
      </div>
      </div>
      {dragged && (
        <div className="tb-ghost" data-testid="tb-ghost" aria-hidden="true" style={{ left: drag.x - drag.offsetX, top: drag.y - drag.offsetY, width: drag.width }}>
          <div className="tb-card-title">{dragged.title}</div>
          <div className="tb-ghost-id">#{dragged.id}</div>
        </div>
      )}
    </div>
  )
}

// The Tasks tab: the board of Personal, with the task sessions beside it.
export default function Tasks({ isActive = true, navData = null }) {
  return <TasksShell isActive={isActive} Board={TaskBoard} sessionHint={navData?.sessionId || null} reportHint={!!navData?.report} />
}
