import { describe, it, expect } from 'vitest'
import { bridgeState, loadDismissed, saveDismissed } from './getStarted.js'

describe('bridgeState', () => {
  const portOpen = { status: 'connected', addr: '127.0.0.1:9222' }
  it('says not paired when the bridge turns this client away', () => {
    expect(bridgeState(portOpen, { running: true, bridge: { status: 'connected' }, clientPairing: 'mismatch' })).toBe('unpaired')
    expect(bridgeState(portOpen, { running: true, bridge: { status: 'unpaired' }, clientPairing: 'ok' })).toBe('unpaired')
  })
  it('says not running when no bridge answers', () => {
    expect(bridgeState(portOpen, { running: false })).toBe('off')
    expect(bridgeState(null, null)).toBe('off')
  })
  it('says connected only when paired and attached', () => {
    expect(bridgeState(portOpen, { running: true, bridge: { status: 'connected' }, clientPairing: 'ok' })).toBe('connected')
    expect(bridgeState(portOpen, { running: true, bridge: { status: 'waiting' }, clientPairing: 'ok' })).toBe('waiting')
  })
  it('falls back to the summary probe when extension status is unknown', () => {
    expect(bridgeState(portOpen, null)).toBe('connected')
  })
})

describe('dismissal', () => {
  it('is remembered and survives a throwing storage', () => {
    const store = new Map()
    const storage = { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) }
    expect(loadDismissed(storage)).toBe(false)
    saveDismissed(storage)
    expect(loadDismissed(storage)).toBe(true)
    const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
    expect(loadDismissed(broken)).toBe(false)
    expect(() => saveDismissed(broken)).not.toThrow()
  })
})
