import { useCallback, useState } from 'react'

// Answered choices/form blocks are remembered in localStorage (per browser
// profile, like the other chat view state), keyed by conversation + message
// (turn id + text part) + the block's source offset in that message, so a
// replay of the history shows them answered. Best effort: storage may be
// unavailable and the block then simply stays answerable.
const KEY = 'monoagent.chatBlocks.answers.v1'
const MAX_ENTRIES = 500

function read() {
  try {
    const v = JSON.parse(globalThis.localStorage.getItem(KEY) || '{}')
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {}
  } catch { return {} }
}

export function blockKey(conversationId, scope, offset) {
  return `${conversationId}|${scope}|${offset}`
}

export function getAnswer(key) {
  const v = read()
  return Object.prototype.hasOwnProperty.call(v, key) ? v[key] : null
}

export function setAnswer(key, answer) {
  const all = read()
  delete all[key]
  all[key] = answer
  const keys = Object.keys(all)
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_ENTRIES))) delete all[k]
  try { globalThis.localStorage.setItem(KEY, JSON.stringify(all)) } catch { /* best effort */ }
}

// useAnswer: [answer, remember]. answer is whatever remember() stored
// (the text that was sent plus block-specific detail), or null.
export function useAnswer(key) {
  const [answer, setLocal] = useState(() => getAnswer(key))
  const remember = useCallback((a) => { setAnswer(key, a); setLocal(a) }, [key])
  return [answer, remember]
}
