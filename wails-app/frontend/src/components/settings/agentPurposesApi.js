import * as GoApp from '../../wailsjs/go/main/App'

// The agent purposes bindings (app_agent_purposes.go), kept apart from
// services/api.js. Each call starts inside a promise, so a synchronous throw
// (no Wails bridge) lands in the caller's catch, and a CLI refusal
// ({"error","code"}) becomes a rejection carrying .code.
function parse(s) {
  const r = JSON.parse(s)
  if (r?.error) {
    const err = new Error(r.error)
    if (r.code) err.code = r.code
    throw err
  }
  return r
}
const call = (fn) => Promise.resolve().then(fn).then(parse)

export const agentPurposesApi = {
  // {purposes:[{id,title,description,categories,configured,pairs,rule,effort,
  //   different_from_author,budget_share_pct,fallback}], available:[pair]}
  list: () => call(() => GoApp.AgentPurposes()),
  // Replaces the purpose's settings with exactly s; pairs are "runtime/model".
  set: (id, s) => call(() => GoApp.SetAgentPurpose(
    id, s.pairs, s.rule, s.effort, s.differentModel, s.budgetSharePct, s.fallback)),
  reset: (id) => call(() => GoApp.ResetAgentPurpose(id)),
  resetAll: () => call(() => GoApp.ResetAgentPurposes()),
}

// pairKey is the "runtime/model" the CLI takes; a runtime's default model is "runtime/".
export const pairKey = (p) => `${p.runtime}/${p.model || ''}`
