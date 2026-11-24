import { describe, it, expect } from 'vitest'
import { POLL_FAIL_LIMIT, nextPollFailures, cancelFailed, pollOutcome } from './runPoll.js'

describe('nextPollFailures', () => {
  it('counts consecutive empty polls and resets on a detail', () => {
    expect(nextPollFailures(0, null)).toBe(1)
    expect(nextPollFailures(3, undefined)).toBe(4)
    expect(nextPollFailures(4, { id: 'x' })).toBe(0)
  })
  it('gives up after the limit', () => {
    let n = 0
    for (let i = 0; i < POLL_FAIL_LIMIT; i++) n = nextPollFailures(n, null)
    expect(n).toBeGreaterThanOrEqual(POLL_FAIL_LIMIT)
  })
})

describe('cancelFailed', () => {
  it('recognises the api wrapper error string', () => {
    expect(cancelFailed('error: Error: boom')).toBe(true)
  })
  it('treats a successful binding result as ok', () => {
    expect(cancelFailed(undefined)).toBe(false)
    expect(cancelFailed(null)).toBe(false)
  })
})

describe('pollOutcome', () => {
  it('keeps polling every live engine status, including WAITING', () => {
    for (const st of ['RUNNING', 'queued', 'PENDING', 'Waiting']) expect(pollOutcome({ status: st })).toEqual({ done: false })
  })
  it('maps a finished status through the shared execStatus table', () => {
    expect(pollOutcome({ status: 'SUCCESS' })).toEqual({ done: true, globalStatus: 'ok' })
    expect(pollOutcome({ status: 'COMPLETED' })).toEqual({ done: true, globalStatus: 'ok' })
    expect(pollOutcome({ status: 'SUCCESS_WITH_ERRORS' })).toEqual({ done: true, globalStatus: 'warning' })
    expect(pollOutcome({ status: 'FAILED' })).toEqual({ done: true, globalStatus: 'error' })
    expect(pollOutcome({ status: 'CANCELLED' })).toEqual({ done: true, globalStatus: 'error' })
    expect(pollOutcome({ status: 'SOMETHING_NEW' })).toEqual({ done: true, globalStatus: 'error' })
  })
})
