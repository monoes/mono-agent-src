import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, onChatEvent, onOrgEvent } from '../../services/api.js'
import {
  newDraftKey, emptySummary, applyChatEvent, summaryFromTurns, moveBubble, loadState, saveState, orgBubble, orgBubbleKey, isOrgBubble,
} from '../../lib/coderBubbles.js'
import { isAccess } from '../../lib/chatAccess.js'
import {
  emptyOrgSummary, applyOrgSummaryEvent, orgBubbleStatus, orgNeedsCount,
} from '../../lib/orgBubble.js'
import { acquireOrgEvents } from '../../lib/orgEventStreams.js'

function logItems(res) {
  if (!res || res.error) return []
  if (Array.isArray(res.items)) return res.items
  if (Array.isArray(res.events)) return res.events
  return Array.isArray(res) ? res : []
}

// rootRoleOf is an org design's boss: its one role that reports to nobody.
function rootRoleOf(design) {
  const roles = Array.isArray(design?.roles) ? design.roles : Array.isArray(design?.org?.roles) ? design.org.roles : []
  const roots = roles.filter(r => r && !r.reports_to && !r.reportsTo)
  return roots.length === 1 ? roots[0].id : ''
}

// useCoderBubbles owns the open coder chat bubbles (#227): the list and its
// order, which one is expanded, each one's collapsed summary, and the
// per-chat things that must survive collapsing (the unsent draft, scroll
// position, the stage divider). It holds one app-wide chat:event
// subscription that updates every open bubble's summary, so a collapsed
// chat costs a few fields, not a mounted transcript.
//
// A bubble is { key, conversationId, cwd, model, runtime, access, title, effort,
// orgMode, taskId, taskKind }. A new coder chat is a
// draft (conversationId '') until its first message creates the
// conversation; its key stays the same after that. A running org opened as
// a bubble (#229) is { key: 'org:<name>', kind: 'org', orgName }: its
// collapsed summary is folded from `org:event` (the orgActivity reducer),
// and the store holds the org's event tail while the bubble is open.
// A circle is one org RUN: { key: 'org:<name>#<runId>', runId } once the run
// is known (the first running event has no id: that bubble is re-keyed in
// place), and a run that is over opens as a read-only { finished: true }
// bubble. An org has at most one live bubble.
export function useCoderBubbles() {
  const initial = useMemo(() => loadState(), [])
  const [bubbles, setBubbles] = useState(initial.bubbles)
  const [side, setSideState] = useState(initial.side)
  const [expandedKey, setExpandedKey] = useState('')
  const [summaries, setSummaries] = useState({})
  const [orgSummaries, setOrgSummaries] = useState({})
  // bubble key → how many HIL / send-approval asks wait for the person (the
  // bus doesn't carry them); polled by useOrgBubbleWatch.
  const [hilNeeds, setHilNeedsState] = useState({})
  // org → its boss's role id, for counting the boss's replies as unread.
  const bosses = useRef({})
  // Per-chat UI state that outlives a collapse: { draft, scrollTop,
  // stageRatio, stageCollapsed }. A ref, not state: writing it never needs a
  // re-render, and reading it happens once, when a chat expands.
  const viewState = useRef({})

  const expandedRef = useRef('')
  expandedRef.current = expandedKey
  const byConversation = useRef({})
  byConversation.current = Object.fromEntries(bubbles.filter(b => b.conversationId).map(b => [b.conversationId, b.key]))
  const byOrg = useRef({})
  const bubblesRef = useRef(bubbles)
  bubblesRef.current = bubbles
  const isLiveOrg = b => isOrgBubble(b) && !b.finished
  byOrg.current = Object.fromEntries(bubbles.filter(isLiveOrg).map(b => [b.orgName, b.key]))
  const orgKey = bubbles.filter(isLiveOrg).map(b => b.orgName).join('\u0000')

  useEffect(() => { saveState({ bubbles, side }) }, [bubbles, side])

  // Restored bubbles: whether each one's latest turn is still running. A
  // conversation deleted since (the CLI reports it not found) loses its
  // bubble.
  useEffect(() => {
    for (const b of initial.bubbles) {
      if (isOrgBubble(b)) continue
      Promise.resolve().then(() => api.getChatTurns(b.conversationId, '', 1))
        .then(res => {
          if (res?.notFound) { dropBubble(b.key); return }
          const items = Array.isArray(res?.items) ? res.items : []
          setSummaries(s => ({ ...s, [b.key]: summaryFromTurns(items) }))
        })
        .catch(() => {})
    }
    // dropBubble only uses state setters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initial])

  useEffect(() => onChatEvent(ev => {
    const key = byConversation.current[ev?.conversationId]
    if (!key) return
    setSummaries(s => ({ ...s, [key]: applyChatEvent(s[key], ev, expandedRef.current === key) }))
  }), [])

  // Every open org bubble holds its org's event tail, and its collapsed
  // summary starts from the current run's log.
  useEffect(() => {
    const names = orgKey ? orgKey.split('\u0000') : []
    const releases = names.map(name => acquireOrgEvents(name))
    return () => releases.forEach(release => release())
  }, [orgKey])

  // runId is the run a finished bubble reads; a live bubble reads the current one.
  const seedOrg = useCallback((name, key, runId = '') => {
    Promise.resolve().then(() => api.getOrgDesign(name))
      .then(design => { const boss = rootRoleOf(design); if (boss) bosses.current[name] = boss })
      .catch(() => {})
    Promise.resolve().then(() => api.getOrgLogs(name, runId))
      .then(res => {
        setOrgSummaries(s => {
          let sum = s[key] || emptyOrgSummary(name)
          for (const ev of logItems(res)) sum = applyOrgSummaryEvent(sum, ev, { expanded: true })
          return { ...s, [key]: sum }
        })
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    for (const b of initial.bubbles) if (isOrgBubble(b)) seedOrg(b.orgName, b.key, b.finished ? b.runId : '')
  }, [initial, seedOrg])

  useEffect(() => onOrgEvent(payload => {
    const key = byOrg.current[payload?.orgName]
    if (!key || !payload.event) return
    const boss = bosses.current[payload.orgName] || ''
    setOrgSummaries(s => {
      const next = applyOrgSummaryEvent(s[key] || emptyOrgSummary(payload.orgName), payload.event, { boss, expanded: expandedRef.current === key })
      return next === s[key] ? s : { ...s, [key]: next }
    })
  }), [])

  const dropBubble = (key) => {
    setBubbles(list => list.filter(b => b.key !== key))
    setSummaries(s => { const next = { ...s }; delete next[key]; return next })
    setOrgSummaries(s => { if (!s[key]) return s; const next = { ...s }; delete next[key]; return next })
    setHilNeedsState(h => { if (!h[key]) return h; const next = { ...h }; delete next[key]; return next })
    delete viewState.current[key]
    setExpandedKey(k => (k === key ? '' : k))
  }

  const expand = useCallback((key) => {
    setExpandedKey(key)
    setSummaries(s => (s[key]?.unread ? { ...s, [key]: { ...s[key], unread: 0 } } : s))
    setOrgSummaries(s => (s[key]?.unread ? { ...s, [key]: { ...s[key], unread: 0 } } : s))
  }, [])

  const collapse = useCallback(() => setExpandedKey(''), [])

  const getView = useCallback((key) => viewState.current[key] || {}, [])
  const setView = useCallback((key, patch) => {
    viewState.current[key] = { ...(viewState.current[key] || {}), ...patch }
  }, [])

  // openConversation shows an existing coder conversation as a bubble,
  // reusing its bubble when it already has one.
  const openConversation = useCallback((conv) => {
    if (!conv?.id) return ''
    const existing = byConversation.current[conv.id]
    if (existing) { expand(existing); return existing }
    const key = conv.id
    setBubbles(list => [...list, {
      key, conversationId: conv.id, cwd: conv.cwd || '', model: conv.model || '', runtime: conv.runtimeId || conv.runtime || '',
      // A history row or a conversation record names its level; a bare { id } (a task's chat) is read from the row later.
      access: isAccess(conv.access) ? conv.access : '', title: conv.title || '', effort: conv.effort || '', orgMode: conv.orgMode === 'dynamic' ? 'dynamic' : '',
      taskId: conv.taskId || '', taskKind: '',
      // Only a record or a history row that names the level makes it known; the pane looks the rest up.
      verified: isAccess(conv.access),
    }])
    Promise.resolve().then(() => api.getChatTurns(conv.id, '', 1))
      .then(res => setSummaries(s => ({ ...s, [key]: summaryFromTurns(Array.isArray(res?.items) ? res.items : []) })))
      .catch(() => {})
    expand(key)
    return key
  }, [expand])

  // openDraft opens a new coder chat. Seeded with { prompt, setup, links,
  // task, conversationId } (a "Send to coder" button, #957), the view starts
  // with the prompt as its draft; a conversationId opens that conversation
  // like openConversation does (the task's chat, already created) and task
  // ({ taskId, goal, status, origin }) puts the task header on it. Called
  // with nothing, or with a click event, it is a blank new chat.
  const openDraft = useCallback((seed) => {
    const opts = seed && typeof seed === 'object' && !seed.nativeEvent && !('target' in seed) ? seed : null
    const key = opts?.conversationId
      ? openConversation({ id: opts.conversationId })
      : (() => {
        const k = newDraftKey()
        setBubbles(list => [...list, { key: k, conversationId: '', cwd: '', model: '', runtime: '', access: '', title: '', effort: '', orgMode: '', taskId: '', taskKind: '' }])
        expand(k)
        return k
      })()
    if (opts) {
      const patch = {}
      if (opts.prompt) patch.draft = String(opts.prompt)
      if (opts.setup) patch.setup = opts.setup
      if (opts.links) patch.links = opts.links
      if (opts.task) {
        patch.task = opts.task
        // The kind outlives a restart on the bubble, so a confined bubble stays confined.
        setBubbles(list => list.map(b => (b.key === key ? { ...b, taskId: opts.task.taskId || b.taskId, taskKind: opts.task.kind || b.taskKind } : b)))
      }
      if (Object.keys(patch).length) setView(key, patch)
    }
    return key
  }, [expand, openConversation, setView])

  // openNewChat opens a blank chat, optionally seeded ({ prompt, setup: {
  // runtime, access, … } }). A stable function, for a toggle that has no
  // props to plumb it through.
  const openNewChat = useCallback((seed) => openDraft(seed || undefined), [openDraft])

  // patchBubble changes fields on one bubble (its access after a change).
  const patchBubble = useCallback((key, patch) => {
    setBubbles(list => list.map(b => (b.key === key ? { ...b, ...patch } : b)))
  }, [])

  // rekeyBubble gives a bubble a new key without remounting it (its slot
  // stays) and moves everything held under the old key.
  const rekeyBubble = useCallback((from, to, patch) => {
    setBubbles(list => list.map(b => (b.key === from ? { ...b, ...patch, key: to } : b)))
    const move = s => { if (!(from in s)) return s; const { [from]: v, ...rest } = s; return { ...rest, [to]: v } }
    setOrgSummaries(move)
    setHilNeedsState(move)
    if (viewState.current[from]) { viewState.current[to] = viewState.current[from]; delete viewState.current[from] }
    setExpandedKey(k => (k === from ? to : k))
    if (expandedRef.current === from) expandedRef.current = to
  }, [])

  // openOrg shows an org run as a bubble. A finished run (finished: true,
  // with its runId) opens read-only under its own key. Otherwise it is the
  // org's one live bubble: reused when it has one, re-keyed to the run when
  // the id arrives, and a bubble of an earlier run becomes a finished one
  // when a new run starts. expand: false adds it collapsed (an org that asks
  // the person something gets a bubble without taking over the screen).
  const openOrg = useCallback((name, { expand: open = true, runId = '', finished = false } = {}) => {
    if (!name) return ''
    if (finished && runId) {
      const key = orgBubbleKey(name, runId)
      const have = bubblesRef.current.some(x => x.key === key)
      if (!have) {
        const b = orgBubble(name, runId, true)
        bubblesRef.current = [...bubblesRef.current, b]
        setBubbles(list => (list.some(x => x.key === key) ? list : [...list, b]))
        seedOrg(name, key, runId)
      }
      if (open) expand(key)
      return key
    }
    const existing = byOrg.current[name]
    const cur = existing && bubblesRef.current.find(x => x.key === existing)
    if (cur && (!runId || cur.runId === runId)) { if (open) expand(existing); return existing }
    if (cur && !cur.runId) {
      const to = orgBubbleKey(name, runId)
      byOrg.current = { ...byOrg.current, [name]: to }
      bubblesRef.current = bubblesRef.current.map(x => (x.key === existing ? { ...x, key: to, runId } : x))
      rekeyBubble(existing, to, { runId })
      if (open) expand(to)
      return to
    }
    if (cur) {
      // A new run: the earlier one stays, read-only.
      bubblesRef.current = bubblesRef.current.map(x => (x.key === existing ? { ...x, finished: true } : x))
      setBubbles(list => list.map(x => (x.key === existing ? { ...x, finished: true } : x)))
      setHilNeedsState(h => { if (!h[existing]) return h; const next = { ...h }; delete next[existing]; return next })
    }
    const b = orgBubble(name, runId)
    byOrg.current = { ...byOrg.current, [name]: b.key }
    bubblesRef.current = [...bubblesRef.current, b]
    setBubbles(list => (list.some(x => x.key === b.key) ? list : [...list, b]))
    seedOrg(name, b.key)
    if (open) expand(b.key)
    return b.key
  }, [expand, seedOrg, rekeyBubble])

  // bindConversation turns a draft into a real conversation once its first
  // message created one.
  const bindConversation = useCallback((key, conv) => {
    byConversation.current = { ...byConversation.current, [conv.id]: key }
    setBubbles(list => list.map(b => (b.key === key ? {
      ...b, conversationId: conv.id, cwd: conv.cwd || b.cwd, model: conv.model || b.model, runtime: conv.runtime || b.runtime,
      access: conv.access || b.access, verified: isAccess(conv.access) || b.verified, effort: conv.effort || b.effort, orgMode: conv.orgMode === 'dynamic' ? 'dynamic' : b.orgMode,
    } : b)))
  }, [])

  // unbindConversation turns a bubble back into a draft: its next message
  // creates a new conversation (Clear / Compact).
  const unbindConversation = useCallback((key) => {
    byConversation.current = Object.fromEntries(Object.entries(byConversation.current).filter(([, k]) => k !== key))
    setBubbles(list => list.map(b => (b.key === key ? { ...b, conversationId: '' } : b)))
  }, [])

  // dropBubble only uses state setters and a ref.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const close = useCallback(dropBubble, [])

  const reorder = useCallback((fromKey, toKey) => setBubbles(list => moveBubble(list, fromKey, toKey)), [])
  const setSide = useCallback((next) => setSideState(next === 'left' ? 'left' : 'right'), [])

  // An org bubble's summary in the coder summary's shape (the dock reads
  // status, unread and cost), plus how many items wait for the person.
  const orgSummaryOf = (key) => {
    const sum = orgSummaries[key]
    const finished = bubbles.find(b => b.key === key)?.finished
    const extra = finished ? 0 : hilNeeds[key] || 0
    if (finished) {
      // A run that is over waits on nobody and works on nothing.
      const cost = sum?.activity.usage.costUsd
      return {
        ...emptySummary(), status: sum && orgBubbleStatus(sum, 0) === 'error' ? 'error' : 'done', unread: sum?.unread || 0,
        costByTurn: cost ? { [sum.activity.run || 'run']: cost } : {}, needs: 0,
      }
    }
    if (!sum) return { ...emptySummary(), status: extra > 0 ? 'needs' : 'idle', needs: extra }
    const cost = sum.activity.usage.costUsd
    return {
      ...emptySummary(), status: orgBubbleStatus(sum, extra), unread: sum.unread,
      costByTurn: cost ? { [sum.activity.run || 'run']: cost } : {}, needs: orgNeedsCount(sum) + extra,
    }
  }
  const coderSummaryOf = (key) => {
    const sum = summaries[key] || emptySummary()
    const extra = hilNeeds[key] || 0
    return extra > 0 ? { ...sum, status: 'needs', needs: extra } : sum
  }

  // setHilNeeds replaces the whole map (key → count) when it differs.
  const setHilNeeds = useCallback((next) => {
    setHilNeedsState(prev => {
      const a = Object.keys(prev), b = Object.keys(next)
      return a.length === b.length && b.every(k => prev[k] === next[k]) ? prev : next
    })
  }, [])

  return {
    bubbles, side, expandedKey, summaries,
    summaryOf: key => (key.startsWith('org:') ? orgSummaryOf(key) : coderSummaryOf(key)),
    setHilNeeds,
    expand, collapse, openDraft, openNewChat, openConversation, openOrg, patchBubble, bindConversation, unbindConversation, close, reorder, setSide,
    getView, setView,
    // bossOf is the org's boss once its design was read ('' before).
    bossOf: name => bosses.current[name] || '',
  }
}
