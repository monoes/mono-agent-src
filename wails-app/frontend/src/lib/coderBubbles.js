// Pure state for the coder chat bubbles (monoes/mono-agent#227): which coder
// chats are open as bubbles, their order, and each one's collapsed summary
// (status ring, unread count, cost) folded from `chat:event`. Everything
// here is plain data in, plain data out, so the rules are unit-testable
// without React or Wails.

import { vaultRelatedCall } from '../components/chat/toolLinks.js'
import { maskCredentials } from '../components/chat/credentials.js'
import { isAccess } from './chatAccess.js'

export const MAX_VISIBLE = 6
export const STORAGE_KEY = 'monoagent:coderBubbles:v1'

// Bubble statuses, in the order they matter to the ring's color.
export const STATUS = {
  idle: 'idle',       // nothing running, nothing new
  working: 'working', // a turn is running
  done: 'done',       // the last turn completed
  error: 'error',     // the last turn failed or was interrupted
  needs: 'needs',     // someone in the chat is waiting on the user
}

let draftSeq = 0

// newDraftKey names a bubble whose conversation doesn't exist yet (a new
// coder chat before its first message).
export function newDraftKey() {
  draftSeq += 1
  return `draft-${Date.now().toString(36)}-${draftSeq}`
}

export function emptySummary() {
  return { status: STATUS.idle, unread: 0, activeTurnId: '', costByTurn: {}, turnStartedAt: '' }
}

// totalCost sums each turn's latest cost snapshot (usage.updated reports a
// running total per turn, never a delta), and each worker's.
export function totalCost(summary) {
  return Object.values(summary?.costByTurn || {}).reduce((n, c) => n + (Number(c) || 0), 0)
}

// costEstimated reports whether any of totalCost is a worker's estimate
// from its tokens (#230), shown as "≈".
export function costEstimated(summary) {
  return Object.values(summary?.estimatedByTurn || {}).some(Boolean)
}

// applyChatEvent folds one chat:event envelope into a bubble's summary.
// expanded says whether the user is looking at that chat right now: a turn
// that finishes while collapsed counts as unread.
export function applyChatEvent(summary, ev, expanded) {
  const s = summary || emptySummary()
  const p = ev?.payload || {}
  switch (ev?.type) {
    case 'turn.started':
      return { ...s, status: STATUS.working, activeTurnId: ev.turnId || '', turnStartedAt: ev.at || '' }
    case 'usage.updated': {
      if (p.costUsd == null || !ev.turnId) return s
      // A dynamic-org worker's running total (#257) is kept under its own
      // key, so the chat's cost adds it to the lead's instead of replacing it.
      const key = p.agentId ? `${ev.turnId}:${p.agentId}` : ev.turnId
      return { ...s, costByTurn: { ...s.costByTurn, [key]: p.costUsd }, estimatedByTurn: { ...s.estimatedByTurn, [key]: !!p.costEstimated } }
    }
    // A dynamic-org agent asking the user something (#228) makes the
    // bubble pulse until that agent moves on.
    case 'agent.message':
      if (p.direction !== 'question' || (s.activeTurnId && ev.turnId && ev.turnId !== s.activeTurnId)) return s
      return { ...s, status: STATUS.needs, needsAgent: p.agentId || '' }
    case 'agent.status':
    case 'agent.finished':
      if (s.status !== STATUS.needs || !s.needsAgent || p.agentId !== s.needsAgent) return s
      return { ...s, status: STATUS.working, needsAgent: '' }
    case 'turn.finished': {
      // A turn other than the running one finishing (a late event for an
      // older turn) must not clear the running one.
      if (s.activeTurnId && ev.turnId && ev.turnId !== s.activeTurnId) return s
      const status = p.status === 'completed' ? STATUS.done
        : p.status === 'cancelled' ? STATUS.idle
          : STATUS.error
      return { ...s, status, activeTurnId: '', turnStartedAt: '', unread: expanded ? 0 : s.unread + 1 }
    }
    default:
      return s
  }
}

// summaryFromTurns builds a restored bubble's summary from `getChatTurns`
// (newest first): the latest turn says whether it is still running.
export function summaryFromTurns(turns) {
  const s = emptySummary()
  const latest = (turns || [])[0]
  if (!latest) return s
  if (latest.status === 'active' && latest.ownedByThisInstance !== false) {
    return { ...s, status: STATUS.working, activeTurnId: latest.id, turnStartedAt: latest.createdAt || '' }
  }
  if (latest.status === 'failed' || latest.status === 'interrupted') return { ...s, status: STATUS.error }
  return s
}

// layoutBubbles splits the open bubbles into the visible ones and an
// overflow list. The expanded bubble always stays visible, so it can be
// found again after switching.
export function layoutBubbles(bubbles, expandedKey, max = MAX_VISIBLE) {
  if (bubbles.length <= max) return { visible: bubbles, overflow: [] }
  const visible = bubbles.slice(0, max - 1)
  const rest = bubbles.slice(max - 1)
  const exp = rest.find(b => b.key === expandedKey)
  if (exp) {
    return { visible: [...visible.slice(0, max - 2), exp], overflow: [...visible.slice(max - 2), ...rest.filter(b => b !== exp)] }
  }
  return { visible, overflow: rest }
}

// The dock's vertical budget. A bubble takes SLOT px (50 + the 10 gap); one with an
// orbit takes ORBIT_EXTRA more (the arc's margin above and below). FIXED is what the
// dock keeps for the rest of the column: the bottom and top gaps, "+", the person
// tray and the grip.
export const DOCK_SLOT = 60
export const DOCK_ORBIT_EXTRA = 132
export const DOCK_FIXED = 172

// fitDock decides which of the already laid-out bubbles the window is tall enough for.
// items: [{ key, orbit }] in dock order (orbit: the bubble has circles around it);
// chip: the list already has a "+N" bubble, which takes a slot. The expanded bubble is
// served first, then the rest in order. A bubble that fits only without its circles is
// shown without them (plain); one that fits not at all goes to the overflow list, so
// the "+" and the person tray are never pushed off the top of the window. At least one
// bubble stays. Returns { visible, overflow, plain } as key lists (plain a Set).
export function fitDock(items, expandedKey, height, chip = false) {
  const need = items.reduce((n, it) => n + DOCK_SLOT + (it.orbit ? DOCK_ORBIT_EXTRA : 0), 0)
  const room = height - DOCK_FIXED - (chip ? DOCK_SLOT : 0)
  if (need <= room) return { visible: items.map(i => i.key), overflow: [], plain: new Set() }
  const budget = room - DOCK_SLOT // the "+N" bubble this will need
  const order = [...items].sort((a, b) => (b.key === expandedKey) - (a.key === expandedKey))
  const keep = new Set()
  const plain = new Set()
  let used = 0
  for (const it of order) {
    const full = DOCK_SLOT + (it.orbit ? DOCK_ORBIT_EXTRA : 0)
    if (used + full <= budget) { keep.add(it.key); used += full } else if (used + DOCK_SLOT <= budget || keep.size === 0) { keep.add(it.key); plain.add(it.key); used += DOCK_SLOT }
  }
  return {
    visible: items.filter(i => keep.has(i.key)).map(i => i.key),
    overflow: items.filter(i => !keep.has(i.key)).map(i => i.key),
    plain,
  }
}

// moveBubble returns bubbles with the one at `from` moved to index `to`.
export function moveBubble(bubbles, fromKey, toKey) {
  const from = bubbles.findIndex(b => b.key === fromKey)
  const to = bubbles.findIndex(b => b.key === toKey)
  if (from < 0 || to < 0 || from === to) return bubbles
  const next = bubbles.slice()
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next
}

// orgBubble is a running org opened as a bubble (#229): chatted with
// through its boss. Its key can never collide with a conversation id.
// A circle is one org RUN: the key is org:<name>#<runId> once the run is
// known, org:<name> before (and for bubbles saved by older builds). finished
// marks a read-only bubble for a run that is over; an org has at most one
// live (not finished) bubble. slot is a React identity that survives the
// key changing when the run id arrives, so nothing remounts.
let slotSeq = 0
export function orgBubbleKey(orgName, runId = '') {
  return runId ? `org:${orgName}#${runId}` : `org:${orgName}`
}

export function orgBubble(orgName, runId = '', finished = false) {
  const id = typeof runId === 'string' ? runId : ''
  slotSeq += 1
  return {
    key: orgBubbleKey(orgName, id), slot: `org-slot-${slotSeq}`, kind: 'org', orgName, runId: id, finished: !!finished && !!id,
    conversationId: '', cwd: '', model: '', runtime: '',
  }
}

export function isOrgBubble(b) {
  return b?.kind === 'org'
}

// ── Persistence ─────────────────────────────────────────────────────────────
// Only real conversations and org bubbles are kept across restarts: a
// draft (no message sent yet) has nothing to come back to. The storage
// accessors can throw (private windows, blocked storage), and the app must
// work without them.

// Bubbles are kept per profile: conversations and orgs belong to one, and a
// shared list would be pruned as "not found" while another profile is active.
export function storageKeyFor(profileId) {
  return profileId ? `${STORAGE_KEY}:${profileId}` : STORAGE_KEY
}

export function loadState(storage = globalThis.localStorage, profileId = '') {
  try {
    const raw = storage?.getItem(storageKeyFor(profileId))
    const parsed = raw ? JSON.parse(raw) : null
    const liveOrgs = new Set()
    const bubbles = Array.isArray(parsed?.bubbles)
      ? parsed.bubbles.map(b => {
        if (b?.kind === 'org' && typeof b.orgName === 'string' && b.orgName) {
          // Entries saved before per-run bubbles have no runId or finished flag.
          const runId = typeof b.runId === 'string' ? b.runId : ''
          let finished = b.finished === true && !!runId
          if (!finished) {
            // Never more than one live bubble per org: a second one is a finished run, or nothing if it has no run.
            if (liveOrgs.has(b.orgName)) { if (!runId) return null; finished = true } else liveOrgs.add(b.orgName)
          }
          return orgBubble(b.orgName, runId, finished)
        }
        if (b && typeof b.conversationId === 'string' && b.conversationId) {
          // No stored level is unknown, never assumed: the pane looks the real one up.
          return {
            key: b.conversationId, conversationId: b.conversationId, cwd: String(b.cwd || ''), model: String(b.model || ''), runtime: String(b.runtime || ''),
            access: isAccess(b.access) ? b.access : '', title: String(b.title || ''), effort: String(b.effort || ''),
            orgMode: b.orgMode === 'dynamic' ? 'dynamic' : '', taskId: String(b.taskId || ''), taskKind: String(b.taskKind || ''),
          }
        }
        return null
      }).filter(Boolean).filter((b, i, all) => all.findIndex(x => x.key === b.key) === i)
      : []
    const side = parsed?.side === 'left' ? 'left' : 'right'
    return { bubbles, side }
  } catch {
    return { bubbles: [], side: 'right' }
  }
}

export function saveState(state, storage = globalThis.localStorage, profileId = '') {
  try {
    // A lite pane (an agent opened from an orbit) is reached from its parent's orbit and is not kept.
    const bubbles = state.bubbles.filter(b => !b.lite && (b.conversationId || (b.kind === 'org' && b.orgName)))
      .map(b => (b.kind === 'org'
        ? { kind: 'org', orgName: b.orgName, runId: b.runId || '', finished: !!b.finished }
        : {
          conversationId: b.conversationId, cwd: b.cwd || '', model: b.model || '', runtime: b.runtime || '',
          // Additive fields: an older build reads the first four and ignores the rest.
          access: b.access || '', title: b.title || '', effort: b.effort || '', orgMode: b.orgMode || '', taskId: b.taskId || '', taskKind: b.taskKind || '',
        }))
    storage?.setItem(storageKeyFor(profileId), JSON.stringify({ bubbles, side: state.side === 'left' ? 'left' : 'right' }))
  } catch {
    // Storage unavailable: bubbles just won't come back after a restart.
  }
}

// ── Click-outside ───────────────────────────────────────────────────────────

// shouldCollapseOnBackdrop decides whether a click that landed on the
// overlay's backdrop collapses it. It doesn't when the press started inside
// the overlay (a drag or text selection that ended outside), when text is
// selected, or while a modal dialog is open on top.
export function shouldCollapseOnBackdrop({ pressStartedOnBackdrop, selectionText, modalOpen }) {
  if (!pressStartedOnBackdrop) return false
  if (selectionText && selectionText.trim()) return false
  if (modalOpen) return false
  return true
}

// monogram is the bubble's two-letter label from a folder name
// ("20260927-brisk-otter" → "BO", "mono-agent" → "MA", "api" → "AP").
// profileFolderName is the name of the profile whose own folder cwd is, or
// '' (#477): a coder chat started in a profile works in that profile's
// folder, usually ~/.monoagent/profiles/<id>, whose last segment is an id
// nobody reads. A folder inside a profile's is named as itself.
export function profileFolderName(cwd, profiles) {
  const norm = p => String(p || '').replace(/[\\/]+$/, '')
  const dir = norm(cwd)
  if (!dir) return ''
  const hit = (profiles || []).find(p => p && p.root_dir && norm(p.root_dir) === dir)
  return hit ? hit.name || '' : ''
}

export function monogram(name) {
  const words = String(name || '').split(/[^A-Za-z]+/).filter(Boolean)
  if (words.length === 0) return '·'
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase()
  return (words[words.length - 2][0] + words[words.length - 1][0]).toUpperCase()
}

// nowDoing names what a live turn is doing right now, from its latest
// running tool call ("Edit · src/app.go", "Bash · go test ./..."), or ''.
// A call that may touch the vault names no target, and a credential in one
// is masked (#872 review); t translates.
export function nowDoing(turn, t = k => k) {
  const running = Object.values(turn?.calls || {}).filter(c => c.status === 'started')
  const call = running[running.length - 1]
  if (!call) return ''
  if (vaultRelatedCall(call)) return `${call.name} · ${t('toolLinks.vaultRelated')}`
  let args = call.arguments
  if (typeof args === 'string') {
    try { args = JSON.parse(args) } catch { args = {} }
  }
  args = args && typeof args === 'object' ? args : {}
  const target = args.file_path || args.path || args.command || args.pattern || args.url || args.description || ''
  const text = maskCredentials(String(target).replace(/\s+/g, ' ').trim())
  return text ? `${call.name} · ${text.length > 60 ? text.slice(0, 59) + '…' : text}` : call.name
}
