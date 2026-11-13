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
  // The validated model roster ({runtimes: [{runtime, version, models: [{model, state}]}]}) the pin lists are made from.
  roster: () => call(() => GoApp.AgentRoster()),
  // The models each installed runtime offers for its --model flag (the scan, then each runtime's own list), for a machine
  // whose roster holds no validated model yet: [{provider, version, models:[id]}]. A runtime whose list fails is left out.
  catalog: async () => {
    const scan = parse(await GoApp.ScanAgentRuntimes())
    const agents = (Array.isArray(scan?.agents) ? scan.agents : []).filter(a => a?.id && a.installed && a.execution_supported !== false)
    const lists = await Promise.all(agents.map(a => Promise.resolve().then(() => GoApp.GetAgentRuntimeModels(a.id, a.binary || '')).then(parse).catch(() => [])))
    return agents.map((a, i) => ({ provider: String(a.id), version: String(a.version || 'unknown'), models: (Array.isArray(lists[i]) ? lists[i] : []).map(m => String(m?.id || '')).filter(Boolean) }))
  },
  // The preview of a charter object: {hash, lines, lift_count, text, phrase_template}. Writes nothing.
  render: (charter, chat) => call(() => GoApp.DoomsdayRender(JSON.stringify(charter), chat?.id || '', chat?.spec ? JSON.stringify(chat.spec) : '')),
  // Starts the run with the phrase the person typed and the token of the preview (start_token). The app also asks the
  // person in a native dialog. chat is {id} of a chat that exists or {spec} of one the start makes after the person said yes
  // (the token covers it; the answer then carries `chat`). The CLI makes the chat's boss, funds it, starts the run and queues the boss's first turn.
  // {ok, started, state: started|not_started|unknown, output, detail, code}.
  start: (charter, phrase, token, chat) => call(() => GoApp.DoomsdayStart(JSON.stringify(charter), phrase, token, chat?.id || '', chat?.spec ? JSON.stringify(chat.spec) : '')),
}
