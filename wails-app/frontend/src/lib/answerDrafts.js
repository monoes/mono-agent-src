// What a person has typed or picked on a question that is not sent yet (#1803):
// card fields, a chat block's picks, a worker question's text. Kept apart from
// the component, so a card that is drawn again (the turn finished, the chat was
// closed and opened, the window reloaded) shows it again. Nothing here is ever
// sent by itself. Held for the window's session only; storage may be
// unavailable, and drafts then last as long as the page.
const KEY = 'monoagent.answerDrafts.v1'
export const MAX_DRAFTS = 100

let memory = {}

function read() {
  try {
    const v = JSON.parse(globalThis.sessionStorage.getItem(KEY) || 'null')
    if (v && typeof v === 'object' && !Array.isArray(v)) return v
  } catch { /* storage unavailable */ }
  return memory
}

function write(all) {
  memory = all
  try { globalThis.sessionStorage.setItem(KEY, JSON.stringify(all)) } catch { /* best effort */ }
}

export function getDraft(key) {
  if (!key) return undefined
  const all = read()
  return Object.hasOwn(all, key) ? all[key] : undefined
}

export function setDraft(key, value) {
  if (!key) return
  const all = { ...read() }
  delete all[key]
  all[key] = value
  const keys = Object.keys(all)
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_DRAFTS))) delete all[k]
  write(all)
}

export function dropDraft(key) {
  if (!key) return
  const all = read()
  if (!Object.hasOwn(all, key)) return
  const next = { ...all }
  delete next[key]
  write(next)
}

// Messages a press asked to send as a chat's next one that could not go yet:
// the person already sent them, so they wait here, per chat and in order, even
// while the chat is closed.
const HELD_KEY = 'monoagent.heldMessages.v1'
let heldMemory = {}

function readHeld() {
  try {
    const v = JSON.parse(globalThis.sessionStorage.getItem(HELD_KEY) || 'null')
    if (v && typeof v === 'object' && !Array.isArray(v)) return v
  } catch { /* storage unavailable */ }
  return heldMemory
}

function writeHeld(all) {
  heldMemory = all
  try { globalThis.sessionStorage.setItem(HELD_KEY, JSON.stringify(all)) } catch { /* best effort */ }
}

export function holdMessage(conversationId, message, { first = false } = {}) {
  const all = readHeld()
  const list = Array.isArray(all[conversationId]) ? all[conversationId] : []
  writeHeld({ ...all, [conversationId]: first ? [message, ...list] : [...list, message] })
}

// Takes the chat's oldest held message, '' when it has none.
export function takeHeld(conversationId) {
  const all = readHeld()
  const [next = '', ...rest] = Array.isArray(all[conversationId]) ? all[conversationId] : []
  if (next) writeHeld({ ...all, [conversationId]: rest })
  return next
}

export function clearDrafts() { write({}); writeHeld({}) }
