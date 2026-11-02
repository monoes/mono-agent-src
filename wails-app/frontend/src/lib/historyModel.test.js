import { describe, it, expect } from 'vitest'
import { bubbleKeyOf, liveStatus, relativeTime, toFilter, reconcileRefresh, orgRunOpening, orgRunLabel, mergePage, deleteOutcome, survivorNames, chatIdsOnly } from './historyModel.js'

describe('historyModel', () => {
  it('keys chats by id and orgs by name', () => {
    expect(bubbleKeyOf({ kind: 'chat', id: 'c1' })).toBe('c1')
    expect(bubbleKeyOf({ kind: 'org', id: 'r1', orgName: 'acme' })).toBe('org:acme')
    expect(bubbleKeyOf({ kind: 'org', id: 'r1', orgName: 'acme', runId: 'r-1' })).toBe('org:acme#r-1')
  })
  it('lets a live running/needs summary override the stored status only', () => {
    const row = { status: 'done' }
    expect(liveStatus(row, null).status).toBe('done')
    expect(liveStatus(row, { status: 'working', unread: 2 })).toMatchObject({ status: 'running', unread: 2 })
    expect(liveStatus(row, { status: 'needs', needs: 1 })).toMatchObject({ status: 'needs', needs: 1 })
    expect(liveStatus({ status: 'error' }, { status: 'idle' }).status).toBe('error')
  })
  it('reads relative times', () => {
    const now = Date.parse('2026-10-22T12:00:00Z')
    expect(relativeTime('2026-10-22T11:59:50Z', now)).toBe('just now')
    expect(relativeTime('2026-10-22T11:55:00Z', now)).toBe('5m ago')
    expect(relativeTime('2026-10-22T09:00:00Z', now)).toBe('3h ago')
    expect(relativeTime('2026-10-20T12:00:00Z', now)).toBe('2d ago')
    expect(relativeTime('2026-09-01T12:00:00Z', now)).toBe('2026-09-01')
    expect(relativeTime('', now)).toBe('')
  })
  it('builds the filter; linked asks the CLI for any application', () => {
    expect(toFilter({ kind: 'chat', query: '  x ' })).toEqual({ kind: 'chat', status: '', application: '', query: 'x' })
    expect(toFilter({ linked: true }).application).toBe('any')
  })
  it('opens the running run live and any other run read-only', () => {
    expect(orgRunOpening({ runId: 'r2', status: 'running' }, { run: 'r2', status: 'running' })).toEqual({ runId: 'r2', finished: false })
    expect(orgRunOpening({ runId: 'r2', status: 'done' }, { run: 'r2', status: 'stopped' })).toEqual({ runId: 'r2', finished: true })
    expect(orgRunOpening({ runId: 'r1', status: 'running' }, { run: 'r2', status: 'running' })).toEqual({ runId: 'r1', finished: true })
    expect(orgRunOpening({ runId: '', status: 'running' }, null)).toEqual({ runId: '', finished: false })
    expect(orgRunOpening({ runId: '', status: 'done' }, { run: '' })).toBeNull()
  })
  it('labels a run', () => {
    expect(orgRunLabel({ runId: 'abcdef123456', updatedAt: '2026-10-20T10:00:00Z' })).toBe('run abcdef12 \u00b7 2026-10-20')
    expect(orgRunLabel({ updatedAt: '2026-10-20T10:00:00Z' })).toBe('2026-10-20')
  })
  it('merges pages without duplicates', () => {
    expect(mergePage([{ id: 'a' }], [{ id: 'a' }, { id: 'b' }]).map(r => r.id)).toEqual(['a', 'b'])
  })
  it('splits a delete result and never lets org runs through', () => {
    expect(deleteOutcome({ deleted: ['a'], active: ['b'], failed: { c: 'boom' } })).toEqual({ deleted: ['a'], active: ['b'], failed: [{ id: 'c', message: 'boom' }], kept: [] })
    expect(chatIdsOnly([{ kind: 'chat', id: 'a' }, { kind: 'org', id: 'o' }])).toEqual(['a'])
  })
  it('keeps apart the chats the CLI declined to delete', () => {
    const out = deleteOutcome({ kept: { a: { code: 'survivors_remain', message: 'm', survivors: [{ pid: 1, command: 'sleep' }] }, b: { code: 'operator_only' } } })
    expect(out.kept).toEqual([
      { id: 'a', code: 'survivors_remain', message: 'm', survivors: [{ pid: 1, command: 'sleep' }] },
      { id: 'b', code: 'operator_only', message: '', survivors: [] },
    ])
    expect(out.failed).toEqual([])
  })
  it('names survivors by command and pid, as many as fit', () => {
    expect(survivorNames([{ pid: 1, command: 'a' }, { pid: 2 }])).toBe('a (1), process (2)')
    expect(survivorNames([1, 2, 3, 4, 5, 6].map(n => ({ pid: n, command: 'c' })))).toBe('c (1), c (2), c (3), c (4), +2')
    expect(survivorNames(undefined)).toBe('')
  })
  it('reconciles a refreshed first page with the loaded rows', () => {
    const r = (id, n) => ({ id, updatedAt: `2026-10-22T10:0${n}:00Z` })
    const prev = [r('a', 9), r('b', 8), r('c', 5), r('d', 1)]
    // b is newer than the page's last row and missing: gone; c and d lie beyond the boundary: kept
    expect(reconcileRefresh(prev, [r('a', 9), r('e', 7)], true).map(x => x.id)).toEqual(['a', 'e', 'c', 'd'])
    // a complete page is the whole list
    expect(reconcileRefresh(prev, [r('a', 9)], false).map(x => x.id)).toEqual(['a'])
  })
})
