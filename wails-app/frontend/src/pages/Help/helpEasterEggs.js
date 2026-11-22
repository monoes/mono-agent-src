// Pure helpers for the Help page's surprises. No React, no timers.
// Full literal i18n keys so the key scan finds them.
export const MASCOT_QUIPS = [
  'help.egg.quip1',
  'help.egg.quip2',
  'help.egg.quip3',
  'help.egg.quip4',
  'help.egg.quip5',
]
export const CONFETTI_AT = 7

const KONAMI = ['ArrowUp', 'ArrowUp', 'ArrowDown', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'ArrowLeft', 'ArrowRight', 'b', 'a']

// Search words that earn a reply instead of (or besides) results.
const SEARCH_EGGS = [
  { match: /^sudo\b/i, key: 'help.egg.sudo' },
  { match: /^rm\s+-rf/i, key: 'help.egg.rmrf' },
  { match: /^(42|meaning of life)$/i, key: 'help.egg.fortyTwo' },
  { match: /^(hello|hi|hey)\b/i, key: 'help.egg.hello' },
]

export function searchEgg(query) {
  const q = String(query || '').trim()
  if (!q) return null
  const hit = SEARCH_EGGS.find(e => e.match.test(q))
  return hit ? hit.key : null
}

// clickQuip(n): key for the nth mascot poke (1-based); cycles.
export function clickQuip(n) {
  const i = Math.max(1, Number(n) || 1) - 1
  return MASCOT_QUIPS[i % MASCOT_QUIPS.length]
}

export function shouldConfetti(n) {
  return n > 0 && n % CONFETTI_AT === 0
}

// createKonami(): feed(key) returns true once the full code has been typed.
export function createKonami() {
  let pos = 0
  return function feed(key) {
    const k = key && key.length === 1 ? key.toLowerCase() : key
    if (k === KONAMI[pos]) pos += 1
    else pos = k === KONAMI[0] ? 1 : 0
    if (pos === KONAMI.length) { pos = 0; return true }
    return false
  }
}

// filterTopics: case-insensitive match over title, blurb and quip.
export function filterTopics(topics, query) {
  const q = String(query || '').trim().toLowerCase()
  if (!q) return topics
  return topics.filter(t => [t.title, t.blurb, t.quip].some(s => String(s || '').toLowerCase().includes(q)))
}
