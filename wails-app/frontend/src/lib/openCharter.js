// The open-ended start form's charter: the pure rules that turn the form into the charter file `doomsday render` and
// `start` read. The CLI validates the charter again and is the authority; this keeps the form honest before it asks.

export const LINES = ['spending', 'destructive_local', 'secrets_vault', 'outbound_third_party', 'install_software', 'host_settings', 'platform_caps']
// The "everything" preset never ticks the platform caps: they are ticked by name only.
export const EVERYTHING_LINES = LINES.filter(l => l !== 'platform_caps')

// The limits the charter fills in when they are left out (internal/cli/doomsday/charter.go), and a careful preset.
export const LIMIT_PRESETS = {
  careful: { tree_depth: 2, live_per_tree: 5, max_concurrent: 5, spawn_rate: 3, open_sessions: 10, max_total: 20 },
  standard: { tree_depth: 4, live_per_tree: 30, max_concurrent: 30, spawn_rate: 6, open_sessions: 50, max_total: 100 },
}
export const LIMIT_KEYS = Object.keys(LIMIT_PRESETS.standard)
// An app start takes goal, criteria and pins of at most MAX_TEXT characters and without a character a person cannot see
// (control other than newline and tab, format such as zero-width, bidi and tag characters, separators, private use,
// unassigned, variation selectors): the CLI refuses them, and so does the form, so Review does not fail on them.
export const MAX_TEXT = 2000
const HIDDEN = /[\p{Cc}\p{Cf}\p{Cs}\p{Co}\p{Cn}\p{Zl}\p{Zp}\uFE00-\uFE0F\u{E0100}-\u{E01EF}\u2800\u3164\u115F\u1160\uFFA0\u034F\u180B-\u180D\u180F\u17B4\u17B5]/u
const ODD_SPACE = /\p{Zs}/u // any space that is not the plain one: no-break, ideographic, em and the like
export const textProblem = s => [...String(s)].length > MAX_TEXT || [...String(s)].some(c => c !== '\n' && c !== '\t' && (HIDDEN.test(c) || (c !== ' ' && ODD_SPACE.test(c))))

export const MAX_TREE_DEPTH = 12 // charter.go MaxTreeDepth
export const ASK_ACCESS = ['scoped', 'coding', 'workspace'] // never full

const emptyPin = () => ({ provider: '', model: '', version: '' })

// The judging policy is a name the charter pins; "default" is the one the app offers. PER_ASK_DEFAULTS is the careful
// start of the per-ask limits and PER_ASK_CHOICES the values the form offers in its lists.
export const JUDGING_POLICIES = ['default']
export const PER_ASK_DEFAULTS = { spendUsd: '1', tasks: '5', depth: '2', runSpendUsd: '10', runTasks: '50' }
export const PER_ASK_CHOICES = {
  spendUsd: ['0.5', '1', '2', '5', '10'],
  tasks: ['1', '3', '5', '10', '20'],
  depth: ['1', '2', '3', '4'],
  runSpendUsd: ['5', '10', '25', '50', '100'],
  runTasks: ['10', '25', '50', '100', '200'],
}

// The budget has no default: choice is '' until the person picks unlimited or a fixed amount.
export const emptyForm = () => ({
  goal: '',
  criteria: [],
  limitsPreset: 'standard',
  limits: { ...LIMIT_PRESETS.standard },
  budget: { choice: '', usd: '' },
  lines: [],
  pins: { boss: emptyPin(), verifier: emptyPin(), judging: JUDGING_POLICIES[0] },
  perAsk: { enabled: false, ...PER_ASK_DEFAULTS, access: 'coding' },
})

// modelChoices turns the validated roster (`agent roster`: {runtimes: [{runtime, version, models: [{model, state}]}]}) into
// the models a pin can name, a runtime being the pin's provider and its version the pin's version. A model that failed its
// test is left out; ready ones come first. An empty list means the form falls back to typing the pin.
export function modelChoices(roster) {
  const out = []
  for (const rr of Array.isArray(roster?.runtimes) ? roster.runtimes : []) {
    if (!rr?.runtime || rr.installed === false) continue
    for (const m of Array.isArray(rr.models) ? rr.models : []) {
      if (!m?.model || m.state === 'failed') continue
      out.push({ provider: String(rr.runtime), model: String(m.model), version: String(rr.version || 'unknown'), ready: m.state === 'ready' })
    }
  }
  return out.sort((a, b) => Number(b.ready) - Number(a.ready))
}

const samePin = (a, b) => a.provider === b.provider && a.model === b.model && a.version === b.version

// defaultPins picks a boss (the first choice) and a verifier that is a different model; with only one model in the
// roster the verifier stays empty and the person picks it (the charter refuses a verifier equal to the boss).
export function defaultPins(choices) {
  const pick = c => (c ? { provider: c.provider, model: c.model, version: c.version } : emptyPin())
  const boss = choices[0]
  const verifier = choices.find(c => boss && !samePin(c, boss))
  return { boss: pick(boss), verifier: pick(verifier) }
}

const DECIMAL = /^\d+(\.\d{1,6})?$/

// usdToMicro is the amount in micro-dollars, computed from the text so that 0.1 is 100000, or NaN when it is not an
// amount with at most six decimals.
export function usdToMicro(usd) {
  const s = String(usd ?? '').trim()
  if (!DECIMAL.test(s)) return NaN
  const [whole, frac = ''] = s.split('.')
  const micro = Number(whole) * 1e6 + Number((frac + '000000').slice(0, 6))
  return Number.isSafeInteger(micro) ? micro : NaN
}

// budgetChoice maps the explicit choice to the charter's limit and to the task session's budget (null: no cap there).
// The cap in force is the smaller of the two (effectiveCapUsd).
export function budgetChoice(choice, usd) {
  if (choice === 'unlimited') return { limits: { no_ceiling: true }, sessionBudgetUsd: null }
  if (choice !== 'fixed') return { error: 'choose unlimited or a fixed amount' }
  const micro = usdToMicro(usd)
  if (!Number.isFinite(micro) || micro <= 0) return { error: 'a fixed budget is an amount above zero' }
  return { limits: { budget_micro: micro }, sessionBudgetUsd: micro / 1e6 }
}

export function effectiveCapUsd(charterUsd, sessionUsd) {
  const caps = [charterUsd, sessionUsd].filter(c => c != null)
  return caps.length ? Math.min(...caps) : null
}

const pinOk = p => !!(p.provider.trim() && p.model.trim() && p.version.trim())
const cleanPin = p => ({ provider: p.provider.trim(), model: p.model.trim(), version: p.version.trim() })
// A whole number that fits a safe integer; a count above MAX_COUNT is refused as unreasonable, not rounded.
const MAX_COUNT = 1000000
const posInt = v => {
  const s = String(v).trim()
  const n = /^\d+$/.test(s) ? Number(s) : NaN
  return Number.isSafeInteger(n) && n <= MAX_COUNT && String(n) === s.replace(/^0+(?=\d)/, '') ? n : NaN
}

function criteriaOf(list, errors) {
  const out = []
  for (const k of list) {
    if (k.kind === 'command' && k.command?.trim() && k.expected?.trim()) out.push({ kind: 'command', command: k.command.trim(), expected: k.expected.trim() })
    else if (k.kind === 'rubric' && k.text?.trim()) out.push({ kind: 'rubric', text: k.text.trim() })
    else errors.criteria = 'every success criterion needs its command and expected result, or its rubric text'
  }
  if (!out.length && !errors.criteria) errors.criteria = 'give at least one success criterion'
  return out
}

function perAskOf(p, errors) {
  if (!p.enabled) return {} // omitted: the boss auto-approves nothing
  const spend = usdToMicro(p.spendUsd), runSpend = usdToMicro(p.runSpendUsd)
  const tasks = posInt(p.tasks), depth = posInt(p.depth), runTasks = posInt(p.runTasks)
  const bad = [spend, runSpend, tasks, depth, runTasks].some(n => !Number.isFinite(n))
  if (bad || !ASK_ACCESS.includes(p.access) || runTasks < Math.max(tasks, 1) || runSpend < spend) {
    errors.perAsk = 'every per-ask limit needs a number, an access that is not full, and run caps at least as large as the per-ask limits'
    return {}
  }
  return { ask_spend_micro: spend, ask_tasks: tasks, ask_access: p.access, ask_depth: depth, run_spend_micro: runSpend, run_tasks: runTasks }
}

// buildCharter is {charter, errors}: the charter object when the form is complete, else null and a message per field.
export function buildCharter(form) {
  const errors = {}
  if (!form.goal.trim()) errors.goal = 'say what the run is for'
  const criteria = criteriaOf(form.criteria, errors)
  const limits = {}
  for (const k of LIMIT_KEYS) {
    const n = posInt(form.limits[k])
    if (!(n >= 1)) errors.limits = `${k} is a whole number of at least 1`
    limits[k] = n
  }
  if (!errors.limits && (limits.tree_depth > MAX_TREE_DEPTH)) errors.limits = `tree_depth is at most ${MAX_TREE_DEPTH}`
  if (!errors.limits && limits.max_concurrent > limits.live_per_tree) errors.limits = 'max concurrent is above live per tree: lower it'
  if (!errors.limits && limits.max_total < limits.live_per_tree) errors.limits = 'max total is below live per tree: raise it'
  const texts = [form.goal, form.pins.judging, ...[form.pins.boss, form.pins.verifier].flatMap(p => [p.provider, p.model, p.version]),
    ...form.criteria.flatMap(k => [k.text, k.command, k.expected])].filter(x => x != null)
  if (texts.some(textProblem)) errors.text = `text of at most ${MAX_TEXT} characters without hidden characters`
  const budget = budgetChoice(form.budget.choice, form.budget.usd)
  if (budget.error) errors.budget = budget.error
  const lines = LINES.filter(l => form.lines.includes(l))
  if (form.lines.some(l => !LINES.includes(l))) errors.lines = 'unknown line'
  const { boss, verifier } = form.pins
  if (!pinOk(boss)) errors.boss = 'pin the boss: provider, model and version'
  if (!pinOk(verifier)) errors.verifier = 'pin the finishing verifier: provider, model and version'
  else if (pinOk(boss) && JSON.stringify(cleanPin(boss)) === JSON.stringify(cleanPin(verifier))) errors.verifier = 'the verifier must be a different model than the boss'
  if (!form.pins.judging.trim()) errors.judging = 'pin the judging policy'
  const perAsk = perAskOf(form.perAsk, errors)
  if (Object.keys(errors).length) return { charter: null, errors }
  return {
    errors,
    charter: {
      goal: form.goal.trim(), criteria, limits: { ...limits, ...budget.limits }, lines,
      pins: { boss: cleanPin(boss), verifier: cleanPin(verifier), judging_policy: form.pins.judging.trim() },
      per_ask: perAsk,
    },
  }
}
