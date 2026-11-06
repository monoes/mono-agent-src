import { describe, it, expect } from 'vitest'
import { LINES, EVERYTHING_LINES, LIMIT_PRESETS, emptyForm, budgetChoice, buildCharter, effectiveCapUsd, usdToMicro } from './openCharter.js'

const pin = (provider, model, version) => ({ provider, model, version })
const good = (over = {}) => ({
  ...emptyForm(),
  goal: 'ship it',
  criteria: [{ kind: 'command', command: 'go test ./...', expected: 'ok' }],
  budget: { choice: 'fixed', usd: '5' },
  pins: { boss: pin('anthropic', 'opus', '4'), verifier: pin('anthropic', 'sonnet', '4'), judging: 'default' },
  ...over,
})

describe('the seven lifted lines', () => {
  it('are the charter\'s names in its order, and "everything" never has the platform caps', () => {
    expect(LINES).toEqual(['spending', 'destructive_local', 'secrets_vault', 'outbound_third_party', 'install_software', 'host_settings', 'platform_caps'])
    expect(EVERYTHING_LINES).toEqual(LINES.slice(0, 6))
  })
})

describe('the budget is chosen, never defaulted', () => {
  it('has no choice at first, and the charter is refused until one is made', () => {
    expect(emptyForm().budget.choice).toBe('')
    const r = buildCharter(good({ budget: { choice: '', usd: '' } }))
    expect(r.charter).toBeNull()
    expect(r.errors.budget).toBeTruthy()
  })
  it('unlimited is no_ceiling for the charter and no cap for the task session', () => {
    expect(budgetChoice('unlimited', '')).toEqual({ limits: { no_ceiling: true }, sessionBudgetUsd: null })
    const r = buildCharter(good({ budget: { choice: 'unlimited', usd: '' } }))
    expect(r.charter.limits.no_ceiling).toBe(true)
    expect(r.charter.limits.budget_micro).toBeUndefined()
  })
  it('a fixed amount is budget_micro for the charter and the same dollars for the task session', () => {
    expect(budgetChoice('fixed', '12.5')).toEqual({ limits: { budget_micro: 12500000 }, sessionBudgetUsd: 12.5 })
    expect(buildCharter(good()).charter.limits.budget_micro).toBe(5000000)
  })
  it('refuses a fixed amount that is empty, zero, negative or not a number', () => {
    for (const usd of ['', '0', '-1', 'abc', '1e400']) expect(budgetChoice('fixed', usd).error, usd).toBeTruthy()
  })
  it('the effective cap is the smaller of the two, and unlimited on both is no cap', () => {
    expect(effectiveCapUsd(5, 3)).toBe(3)
    expect(effectiveCapUsd(null, 3)).toBe(3)
    expect(effectiveCapUsd(5, null)).toBe(5)
    expect(effectiveCapUsd(null, null)).toBeNull()
  })
  it('converts dollars to micro-dollars without float drift', () => {
    expect(usdToMicro('0.1')).toBe(100000)
    expect(usdToMicro('19.99')).toBe(19990000)
    expect(usdToMicro('0.0000001')).toBeNaN() // finer than a micro-dollar is refused, not rounded
  })
})

describe('buildCharter', () => {
  it('builds the charter file the CLI reads, with the defaults of the standard preset', () => {
    const { charter, errors } = buildCharter(good({ lines: ['spending'] }))
    expect(errors).toEqual({})
    expect(charter).toEqual({
      goal: 'ship it',
      criteria: [{ kind: 'command', command: 'go test ./...', expected: 'ok' }],
      limits: { tree_depth: 4, live_per_tree: 30, max_concurrent: 30, spawn_rate: 6, open_sessions: 50, max_total: 100, budget_micro: 5000000 },
      lines: ['spending'],
      pins: { boss: pin('anthropic', 'opus', '4'), verifier: pin('anthropic', 'sonnet', '4'), judging_policy: 'default' },
      per_ask: {},
    })
    expect(LIMIT_PRESETS.standard).toEqual({ tree_depth: 4, live_per_tree: 30, max_concurrent: 30, spawn_rate: 6, open_sessions: 50, max_total: 100 })
  })
  it('needs a goal, at least one success criterion, and pins that differ', () => {
    expect(buildCharter(good({ goal: '  ' })).errors.goal).toBeTruthy()
    expect(buildCharter(good({ criteria: [] })).errors.criteria).toBeTruthy()
    expect(buildCharter(good({ criteria: [{ kind: 'rubric', text: '' }] })).errors.criteria).toBeTruthy()
    expect(buildCharter(good({ criteria: [{ kind: 'rubric', text: 'reads well' }] })).charter.criteria).toEqual([{ kind: 'rubric', text: 'reads well' }])
    const same = pin('a', 'm', '1')
    expect(buildCharter(good({ pins: { boss: same, verifier: { ...same }, judging: 'j' } })).errors.verifier).toBeTruthy()
    expect(buildCharter(good({ pins: { boss: pin('', '', ''), verifier: same, judging: 'j' } })).errors.boss).toBeTruthy()
    expect(buildCharter(good({ pins: { boss: same, verifier: pin('a', 'n', '1'), judging: '' } })).errors.judging).toBeTruthy()
  })
  it('lines are only what is ticked, by name, and an unknown name is refused', () => {
    expect(buildCharter(good()).charter.lines).toEqual([])
    expect(buildCharter(good({ lines: ['host_settings', 'spending'] })).charter.lines).toEqual(['spending', 'host_settings'])
    expect(buildCharter(good({ lines: ['everything'] })).errors.lines).toBeTruthy()
  })
  it('per-ask limits are omitted (deny) unless switched on, and then need every number, a bounded access and run caps', () => {
    expect(buildCharter(good()).charter.per_ask).toEqual({})
    const on = { enabled: true, spendUsd: '1', tasks: '2', access: 'coding', depth: '1', runSpendUsd: '5', runTasks: '10' }
    expect(buildCharter(good({ perAsk: on })).charter.per_ask).toEqual({ ask_spend_micro: 1000000, ask_tasks: 2, ask_access: 'coding', ask_depth: 1, run_spend_micro: 5000000, run_tasks: 10 })
    expect(buildCharter(good({ perAsk: { ...on, access: 'full' } })).errors.perAsk).toBeTruthy()
    expect(buildCharter(good({ perAsk: { ...on, runSpendUsd: '0.5' } })).errors.perAsk).toBeTruthy()
    expect(buildCharter(good({ perAsk: { ...on, runTasks: '1' } })).errors.perAsk).toBeTruthy()
    expect(buildCharter(good({ perAsk: { ...on, tasks: '' } })).errors.perAsk).toBeTruthy()
  })
  it('max concurrent above live per tree is refused, not chosen for the person', () => {
    const limits = { ...LIMIT_PRESETS.standard, max_concurrent: 31 }
    expect(buildCharter(good({ limits })).errors.limits).toBeTruthy()
  })
})
