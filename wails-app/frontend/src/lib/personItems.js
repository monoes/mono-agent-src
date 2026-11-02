// Pure model of the items that wait for the person (`task session person list`): which
// are new, whether a new one sounds the chime, which agent's circle they light up and
// which pane shows them. No React, no DOM, no audio.

export const CHIME_DEBOUNCE_MS = 5000
export const SEEN_KEEP = 200

// planAlert decides what a fresh list of items does. seen is the ids already announced
// (a Set); an id is announced once, ever, so an item that stays open never chimes
// again. A chime sounds for new ids when sound is on and the last one was at least
// CHIME_DEBOUNCE_MS ago; ids that arrive inside the window are still marked seen (a
// burst is one chime, not a chime later for a leftover). Returns the new seen set, the
// new items (for the OS notification) and whether to chime.
export function planAlert({ items, seen, lastChimeAt = 0, now, soundOn = true }) {
  const list = Array.isArray(items) ? items : []
  const known = seen instanceof Set ? seen : new Set(seen || [])
  const fresh = list.filter(it => it && it.id != null && !known.has(it.id))
  if (fresh.length === 0) return { seen: known, fresh, chime: false, lastChimeAt }
  const next = new Set(known)
  fresh.forEach(it => next.add(it.id))
  const chime = !!soundOn && now - lastChimeAt >= CHIME_DEBOUNCE_MS
  return { seen: next, fresh, chime, lastChimeAt: chime ? now : lastChimeAt }
}

// trimSeen keeps the newest SEEN_KEEP ids (ids grow with time) so the stored set stays small.
export function trimSeen(seen) {
  return [...seen].filter(n => Number.isFinite(n)).sort((a, b) => a - b).slice(-SEEN_KEEP)
}

// itemsForConversation: the items that belong in one chat's pane, that is the items about the
// session this chat is the agent of.
export function itemsForConversation(items, conversationId) {
  if (!conversationId) return []
  return (items || []).filter(it => it?.conversationId === conversationId)
}

// budgetSharePercent renders the budget share as a whole percent, or '' when unknown.
export function budgetSharePercent(item) {
  const s = Number(item?.budgetShare)
  return Number.isFinite(s) && s > 0 ? String(Math.round(s * 100)) : ''
}
