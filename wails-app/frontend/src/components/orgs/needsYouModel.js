// Joins `org autonomy needs-you` items (routed to a human by the decision
// service) with the raw pending lists monomind keeps (questions, gates,
// approvals), which carry what the resolve actions need. Pure.
import { classOfAction } from './autonomyModel.js'
import { toMillis } from './waiting.js'

function list(payload, key) {
  if (!payload || payload.error) return []
  if (Array.isArray(payload[key])) return payload[key]
  if (Array.isArray(payload.items)) return payload.items
  if (Array.isArray(payload)) return payload
  return []
}

/**
 * Pending raw items, one per resolvable unit. Approvals are grouped by
 * (role, action) because `org approve|deny` resolves every pending entry for
 * that pair at once.
 */
export function pendingRawItems(questionsRes, gatesRes, approvalsRes) {
  const out = []
  for (const q of list(questionsRes, 'questions')) {
    if (q.answer != null || q.answeredAt) continue
    out.push({ kind: 'question', key: `question:${q.questionId}`, ref: q.questionId, role: q.role ?? null, since: toMillis(q.ts), text: q.question ?? '', raw: q })
  }
  for (const g of list(gatesRes, 'gates')) {
    if (g.status && g.status !== 'pending') continue
    out.push({ kind: 'gate', key: `gate:${g.id}`, ref: g.id, role: g.roleId ?? null, since: toMillis(g.createdAt), text: g.name ?? g.id, raw: g })
  }
  const groups = new Map()
  for (const a of list(approvalsRes, 'approvals')) {
    if (a.approved !== null && a.approved !== undefined) continue
    const key = `approval:${a.roleId}:${a.action}`
    const g = groups.get(key)
    const since = toMillis(a.ts)
    const commands = a.command ? [{ command: a.command, truncated: !!a.command_truncated, cwd: a.cwd }] : []
    if (!g) {
      groups.set(key, {
        kind: 'approval', key, ref: `${a.roleId}:${a.action}`, role: a.roleId ?? null, action: a.action,
        since, count: 1, requestIds: a.requestId ? [a.requestId] : [], text: a.question ?? `Approve ${a.action}?`, raw: a, commands,
      })
    } else {
      g.count++
      if (since != null && (g.since == null || since < g.since)) g.since = since
      if (a.requestId) g.requestIds.push(a.requestId)
      g.commands.push(...commands)
    }
  }
  out.push(...groups.values())
  return out.sort((x, y) => (x.since ?? 0) - (y.since ?? 0))
}

function matches(item, raw) {
  const kind = item.kind === 'ask_human' ? 'question' : item.kind
  if (kind !== raw.kind) return false
  if (kind === 'question' || kind === 'gate') return item.ref === raw.ref
  if (kind === 'approval') {
    if (item.ref && (item.ref === raw.ref || raw.requestIds.includes(item.ref))) return true
    return item.requester === raw.role && item.class === classOfAction(raw.action)
  }
  return false
}

/**
 * Split pending work into what needs the operator (needs-you items, each
 * joined to its raw item when one exists) and what autonomy is handling.
 * When needs-you is unavailable every raw item is treated as needing you.
 */
export function joinNeedsYou(needsYouRes, rawItems) {
  const available = !!needsYouRes && !needsYouRes.error && Array.isArray(needsYouRes.items)
  if (!available) {
    return { available: false, mine: rawItems.map(raw => ({ item: null, raw })), others: [] }
  }
  const used = new Set()
  const mine = needsYouRes.items.map(item => {
    const raw = rawItems.find(r => !used.has(r.key) && matches(item, r)) || null
    if (raw) used.add(raw.key)
    return { item, raw }
  })
  const others = rawItems.filter(r => !used.has(r.key))
  return { available: true, mine, others }
}
