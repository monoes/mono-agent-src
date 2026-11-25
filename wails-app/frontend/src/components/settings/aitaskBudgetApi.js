import * as GoApp from '../../wailsjs/go/main/App'

// The AI task budget bindings (app_aitask_budget.go), kept apart from
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

export const aitaskBudgetApi = {
  // {operatorUsd, caps:[{scope,key,capUsd,usedUsd}], spentUsd}
  list: () => call(() => GoApp.AITaskBudgets()),
  set: (scope, key, usd) => call(() => GoApp.SetAITaskBudget(scope, key, usd)),
  clear: (scope, key) => call(() => GoApp.ClearAITaskBudget(scope, key)),
  // {taskId, capUsd, spentUsd, reported}
  // Lowers a chat's own cap, never raises it: scope 'task' (key = task id) or 'conversation' (key = conversation id, creates the chat's own cap).
  lower: (scope, key, usd) => call(() => GoApp.LowerAITaskBudget(scope, key, usd)),
  forTask: (taskId) => call(() => GoApp.AITaskBudgetFor(taskId)),
  // {bounded, capUsd, source, totalUsd?, usedUp?, taskId?, unenforceableOn?, workersCapUsd?}
  spendCap: (conversationId) => call(() => GoApp.ChatSpendCap(conversationId)),
}

// money formats an amount of USD for a card.
export const money = (usd) => `$${Number(usd || 0).toFixed(usd >= 1 || usd === 0 ? 2 : 4)}`
