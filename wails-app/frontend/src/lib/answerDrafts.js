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

export function clearDrafts() { write({}) }
