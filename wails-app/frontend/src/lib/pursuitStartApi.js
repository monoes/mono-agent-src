import * as GoApp from '../wailsjs/go/main/App'

// The pursuit start bindings (wails-app/app_pursuit_start.go). Each call starts inside a promise, so a synchronous throw
// (no Wails bridge) lands in the caller's catch, and each answer is the CLI's document as an object. A refusal is
// {error, code}; the start's own answer is {ok, started, state, output, detail, code}.
const parse = s => {
  try { return JSON.parse(s) } catch { return { error: String(s || '') } }
}
const call = fn => Promise.resolve().then(fn).then(parse)

export const pursuitStartApi = {
  // The preview the CLI wrote, plus start_token: goal, limits as resolved, cost rows, host, blockers, phrase_template.
  // A limit of 0 is the CLI's default. Writes nothing.
  preview: (sessionId, limits) => call(() => GoApp.PursuitStartPreview(sessionId, limits)),
  // Confirms the goal and starts the pursuit with the three limits the preview resolved, the phrase the person typed and
  // the token of that preview. The app also asks the person in a native dialog.
  start: (sessionId, limits, phrase, token) => call(() => GoApp.PursuitStart(sessionId, limits, phrase, token)),
}

// The phrase to type is the CLI's template with its five placeholders filled with values from the same preview. It is shown
// to the person and never put in the input for them.
export function expectedPhrase(preview) {
  const hash = preview?.goal?.hash
  const lim = preview?.limits
  if (typeof preview?.phrase_template !== 'string' || typeof hash !== 'string' || hash.length < 8 || !lim) return ''
  return preview.phrase_template
    .replace('<ID>', String(preview.session_id))
    .replace('<first 8 characters of the hash>', hash.slice(0, 8))
    .replace('<percent>', String(lim.pct))
    .replace('<parked>', String(lim.parked_max))
    .replace('<wakes>', String(lim.wakes_max))
}

// The sane range of an away limit the page lets through; the CLI decides the real one and its refusal is shown.
export const LIMIT_MAX = { parkedMax: 1_000_000, pct: 100, wakesMax: 1_000_000 }
// limitOk: '' (the CLI's default) or a whole number from 1 up.
export const limitOk = (key, v) => v === '' || (/^\d{1,7}$/.test(v) && Number(v) >= 1 && Number(v) <= LIMIT_MAX[key])
