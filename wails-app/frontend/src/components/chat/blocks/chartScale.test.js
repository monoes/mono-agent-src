import { describe, it, expect } from 'vitest'
import { niceTicks, fmtCompact, bandIndex, labelStep, barPath, arcPath } from './chartScale.js'
import { validateChart } from './chartSchema.js'
import { parseBlock } from './parse.js'

describe('chart scale', () => {
  it('niceTicks gives round values covering the range', () => {
    expect(niceTicks(0, 87, 4)).toEqual([0, 50, 100])
    expect(niceTicks(0, 10, 5)).toEqual([0, 2, 4, 6, 8, 10])
    const t = niceTicks(-3, 7, 4)
    expect(t[0]).toBeLessThanOrEqual(-3)
    expect(t[t.length - 1]).toBeGreaterThanOrEqual(7)
    expect(niceTicks(5, 5).length).toBeGreaterThan(1)
  })
  it('fmtCompact is full under 10k and compact above', () => {
    expect(fmtCompact(1234)).toBe('1234')
    expect(fmtCompact(12900)).toBe('12.9K')
    expect(fmtCompact(4200000)).toBe('4.2M')
    expect(fmtCompact(0.125)).toBe('0.13')
  })
  it('bandIndex clamps', () => {
    expect(bandIndex(-5, 0, 100, 4)).toBe(0)
    expect(bandIndex(60, 0, 100, 4)).toBe(2)
    expect(bandIndex(500, 0, 100, 4)).toBe(3)
  })
  it('labelStep thins long labels', () => {
    expect(labelStep(['a', 'b'], 50)).toBe(1)
    expect(labelStep(['2026-11-01', '2026-11-02'], 20)).toBeGreaterThan(1)
  })
  it('paths are non-empty strings (zero-height bar is empty)', () => {
    expect(barPath(0, 10, 100, 50)).toMatch(/^M/)
    expect(barPath(0, 10, 100, 150)).toMatch(/^M/)
    expect(barPath(0, 10, 100, 100)).toBe('')
    expect(arcPath(50, 50, 30, 45, 0, Math.PI)).toMatch(/^M/)
  })
})

describe('chart schema', () => {
  const st = (o) => parseBlock('chart', JSON.stringify(o), { chart: validateChart }).status
  it('accepts each type', () => {
    expect(st({ type: 'bar', x: ['a', 'b'], series: [{ name: 's', data: [1, 2] }] })).toBe('ok')
    expect(st({ type: 'line', x: ['a'], series: [{ name: 's', data: [1] }] })).toBe('ok')
    expect(st({ type: 'donut', slices: [{ label: 'a', value: 1 }, { label: 'b', value: 0 }] })).toBe('ok')
    expect(st({ type: 'sparkline', data: [1, 2, 3] })).toBe('ok')
    expect(st({ type: 'stat', stats: [{ label: 'Runs', value: 12, delta: '+3%', trend: 'up' }] })).toBe('ok')
  })
  it('rejects bad shapes and caps', () => {
    expect(st({ type: 'pie' })).toBe('invalid')
    expect(st({ type: 'bar', x: ['a', 'b'], series: [{ name: 's', data: [1] }] })).toBe('invalid')
    expect(st({ type: 'bar', x: Array.from({ length: 61 }, (_, i) => `${i}`), series: [{ name: 's', data: Array(61).fill(1) }] })).toBe('invalid')
    expect(st({ type: 'bar', x: ['a'], series: Array.from({ length: 7 }, () => ({ name: 's', data: [1] })) })).toBe('invalid')
    expect(st({ type: 'bar', x: ['a'], series: [{ name: 's', data: ['1'] }] })).toBe('invalid')
    expect(st({ type: 'donut', slices: [{ label: 'a', value: -1 }] })).toBe('invalid')
    expect(st({ type: 'donut', slices: [{ label: 'a', value: 0 }] })).toBe('invalid')
    expect(st({ type: 'sparkline', data: [1] })).toBe('invalid')
    expect(st({ type: 'stat', stats: [{ label: 'x', value: 1, trend: 'sideways' }] })).toBe('invalid')
  })
  it('rejects non-finite numbers (JSON cannot carry them, but 1e999 parses to Infinity)', () => {
    expect(parseBlock('chart', '{"type":"sparkline","data":[1,1e999]}', { chart: validateChart }).status).toBe('invalid')
  })
})
