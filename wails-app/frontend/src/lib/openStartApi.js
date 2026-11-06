import * as GoApp from '../wailsjs/go/main/App'

// The open-ended start form's bindings (app_doomsday_start.go). Each call starts inside a promise, so a synchronous
// throw (no Wails bridge) lands in the caller's catch, and each answer is the CLI's document as an object. A refusal
// is {error, code}; the start's own answer is {ok, started, output, detail, code}.
const parse = s => {
  try { return JSON.parse(s) } catch { return { error: String(s || '') } }
}
const call = fn => Promise.resolve().then(fn).then(parse)

export const openStartApi = {
  // {mode_available, reason, run_exists, platform, not_defended}: whether a run can start here, and why not.
  entry: () => call(() => GoApp.DoomsdayOpenEntry()),
  // The preview of a charter object: {hash, lines, lift_count, text, phrase_template}. Writes nothing.
  render: charter => call(() => GoApp.DoomsdayRender(JSON.stringify(charter))),
  // Starts the run with the phrase the person typed. bossSession is the id of the boss session, 0 for none.
  start: (charter, phrase, bossSession = 0) => call(() => GoApp.DoomsdayStart(JSON.stringify(charter), phrase, bossSession)),
}
