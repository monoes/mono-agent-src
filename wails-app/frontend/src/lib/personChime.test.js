// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TONES, getNotifySound, playChime, setNotifySound, subscribeNotifySound } from './personChime.js'

function fakeContext(state = 'running') {
  const log = { osc: [], resumed: 0 }
  const param = () => ({ value: 0, setValueAtTime: vi.fn(), exponentialRampToValueAtTime: vi.fn() })
  return {
    log,
    state,
    currentTime: 10,
    destination: {},
    resume() { log.resumed++; return Promise.resolve() },
    createGain: () => ({ gain: param(), connect: vi.fn() }),
    createOscillator: () => {
      const o = { type: '', frequency: { value: 0 }, connect: vi.fn(), start: vi.fn(), stop: vi.fn() }
      log.osc.push(o)
      return o
    },
  }
}

afterEach(() => { localStorage.clear(); setNotifySound(true) })

describe('playChime', () => {
  it('schedules two tones at the two frequencies', () => {
    const ctx = fakeContext()
    expect(playChime(() => ctx)).toBe(true)
    expect(ctx.log.osc.map(o => o.frequency.value)).toEqual(TONES.map(t => t.freq))
    expect(ctx.log.osc[1].start).toHaveBeenCalledWith(10 + TONES[1].at)
  })

  it('asks a suspended (blocked) context to resume, and still never throws', () => {
    const ctx = fakeContext('suspended')
    expect(playChime(() => ctx)).toBe(true)
    expect(ctx.log.resumed).toBe(1)
  })

  it('a rejected resume is swallowed', async () => {
    const ctx = fakeContext('suspended')
    ctx.resume = () => Promise.reject(new Error('blocked'))
    expect(() => playChime(() => ctx)).not.toThrow()
    await Promise.resolve()
  })

  it('falls back to silence when there is no audio, or the audio throws', () => {
    expect(playChime(() => null)).toBe(false)
    expect(playChime(() => { throw new Error('NotAllowedError') })).toBe(false)
    const broken = fakeContext()
    broken.createOscillator = () => { throw new Error('closed') }
    expect(playChime(() => broken)).toBe(false)
  })
})

describe('notifySound setting', () => {
  it('is on by default and remembers off', () => {
    expect(getNotifySound()).toBe(true)
    setNotifySound(false)
    expect(getNotifySound()).toBe(false)
    expect(localStorage.getItem('monoagent:notifySound')).toBe('off')
    setNotifySound(true)
    expect(getNotifySound()).toBe(true)
  })

  it('tells subscribers, and one that throws does not stop the rest', () => {
    const seen = []
    const off1 = subscribeNotifySound(() => { throw new Error('x') })
    const off2 = subscribeNotifySound(v => seen.push(v))
    setNotifySound(false)
    off1(); off2()
    setNotifySound(true)
    expect(seen).toEqual([false])
  })

  it('still works when storage throws', () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
    setNotifySound(false)
    expect(getNotifySound()).toBe(false)
    setNotifySound(true)
    expect(getNotifySound()).toBe(true)
    spy.mockRestore(); set.mockRestore()
  })
})
