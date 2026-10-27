import { describe, expect, it } from 'vitest'
import { actionsFor, awaitingApproval, boardEditable, chipState, nearBudget, planTree, progress, selectionAfter, spendParts, waitingOn } from './taskSessionModel.js'
import { normalizeBoard } from './taskModel.js'

const s = (state, extra = {}) => ({ id: 1, state, archived: false, counts: { done: 2, total: 7 }, spent_usd: 1.2, budget_usd: 5, ...extra })

describe('taskSessionModel', () => {
  it('offers the actions each state allows, and none for an archived session', () => {
    expect(actionsFor(s('draft'))).toContain('approve')
    expect(actionsFor(s('running'))).toEqual(['pause', 'close', 'stop', 'budget'])
    expect(actionsFor(s('paused'))).toContain('resume')
    for (const st of ['done', 'stopped', 'failed']) {
      expect(actionsFor(s(st))).toEqual(['archive', 'budget'])
    }
    for (const st of ['running', 'paused', 'stopped']) expect(actionsFor(s(st))).not.toContain('approve')
    expect(actionsFor(s('draft', { archived: true }))).toEqual([])
  })

  it('formats progress and spend', () => {
    expect(progress(s('running'))).toBe('2/7')
    expect(spendParts(s('running'))).toEqual({ spent: '$1.20', budget: '$5.00' })
    expect(spendParts(s('running', { budget_usd: 0 })).budget).toBe('')
    expect(nearBudget(s('running', { spent_usd: 4.5 }))).toBe(true)
    expect(nearBudget(s('running', { budget_usd: 0, spent_usd: 100 }))).toBe(false)
  })

  it('reads an unknown state as draft and only a running board as editable', () => {
    expect(chipState({ state: 'weird' })).toBe('draft')
    expect(boardEditable(s('running'))).toBe(true)
    expect(boardEditable(null)).toBe(true)
    expect(boardEditable(s('draft'))).toBe(false)
  })

  it('counts the drafts awaiting approval, archived ones out', () => {
    expect(awaitingApproval([s('draft'), s('draft', { archived: true }), s('running')])).toBe(1)
  })

  it('lists the dependencies that are not done', () => {
    const board = normalizeBoard({ tasks: { ready: [{ id: 2, position: 1 }], done: [{ id: 1, position: 1 }], review: [{ id: 3, position: 1 }] } })
    expect(waitingOn({ depends_on: [1, 2, 3] }, board)).toEqual([2, 3])
    expect(waitingOn({ depends_on: [1] }, board)).toEqual([])
    expect(waitingOn({}, board)).toEqual([])
  })

  it('builds the plan tree from parents, keeping the plan order', () => {
    const tree = planTree([{ id: 1, title: 'epic' }, { id: 2, parent_id: 1 }, { id: 3, parent_id: 1 }, { id: 4, parent_id: 99 }])
    expect(tree.map(n => n.task.id)).toEqual([1, 4])
    expect(tree[0].children.map(n => n.task.id)).toEqual([2, 3])
  })

  it('keeps the chosen session only while it is listed', () => {
    expect(selectionAfter(1, [s('draft')])).toBe(1)
    expect(selectionAfter(2, [s('draft')])).toBe(null)
    expect(selectionAfter(null, [s('draft')])).toBe(null)
  })
})
