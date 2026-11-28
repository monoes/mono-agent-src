// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, act, cleanup } from '@testing-library/react'
import { A2UISurface, MAX_NODES, planTree } from './A2UISurface.jsx'
import { applySurfaceEvent } from '../../lib/a2uiModel.js'
import { hilApproval, structuredQuestion, missionPlan } from './fixtures.js'
import '../../i18n.js'

const fold = payloads => payloads.reduce((s, p, i) => applySurfaceEvent(s, p, i + 1), [])[0]
const mk = (components, extra = {}) => ({ surfaceId: 's', provenance: 'agent', components: Object.fromEntries(components.map(c => [c.id, c])), dataModel: {}, status: 'live', lastSeq: 1, ...extra })

afterEach(cleanup)

describe('A2UISurface', () => {
  it('renders the fixtures and labels provenance', () => {
    const { unmount } = render(<A2UISurface surface={fold(hilApproval)} />)
    expect(screen.getByText('from monoagent')).toBeTruthy()
    expect(screen.getByLabelText('Reply').value).toMatch(/Thanks Sara/)
    expect(screen.getByRole('button', { name: 'Approve and send' })).toBeTruthy()
    unmount()
    render(<A2UISurface surface={fold(structuredQuestion)} />)
    expect(screen.getByText('from agent')).toBeTruthy()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
    expect(screen.getByRole('radio', { name: /Founders/ }).checked).toBe(true)
  })

  it('renders progress, badge, divider, caption', () => {
    render(<A2UISurface surface={fold(missionPlan)} />)
    const bar = screen.getByRole('progressbar')
    expect(bar.getAttribute('aria-valuenow')).toBe('1.8')
    expect(bar.getAttribute('aria-valuemax')).toBe('5')
    expect(screen.getByText('needs you')).toBeTruthy()
  })

  it('two-way binds locally and builds the action envelope', () => {
    const onAction = vi.fn()
    render(<A2UISurface surface={fold(hilApproval)} onAction={onAction} />)
    fireEvent.change(screen.getByLabelText('Reply'), { target: { value: 'edited' } })
    expect(screen.getByLabelText('Reply').value).toBe('edited')
    fireEvent.click(screen.getByRole('button', { name: 'Approve and send' }))
    expect(onAction).toHaveBeenCalledTimes(1)
    const env = onAction.mock.calls[0][0]
    expect(env.version).toBe('1.0')
    expect(env.action).toMatchObject({ name: 'hil.approve', surfaceId: 'hil-482', sourceComponentId: 'approve', context: { id: '482', text: 'edited' } })
    expect(new Date(env.action.timestamp).toString()).not.toBe('Invalid Date')
  })

  it('checkbox and choice picker write the local model into action context', () => {
    const onAction = vi.fn()
    render(<A2UISurface surface={fold(structuredQuestion)} onAction={onAction} />)
    fireEvent.click(screen.getByRole('radio', { name: /Engineers/ }))
    fireEvent.change(screen.getByLabelText('Optional note'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onAction.mock.calls[0][0].action.context).toEqual({ audience: ['engineers'], note: 'hi' })
    const s = mk([{ id: 'root', component: 'CheckBox', label: 'Agree', value: { '@path': '/a' } }], { dataModel: { a: false } })
    render(<A2UISurface surface={s} />)
    fireEvent.click(screen.getByLabelText('Agree'))
    expect(screen.getByLabelText('Agree').checked).toBe(true)
  })

  describe('double submit guard', () => {
    beforeEach(() => vi.useFakeTimers())
    afterEach(() => vi.useRealTimers())
    it('disables after a click, re-enables after 3s or a surface change', () => {
      const onAction = vi.fn()
      const surface = fold(hilApproval)
      const { rerender } = render(<A2UISurface surface={surface} onAction={onAction} />)
      const b = screen.getByRole('button', { name: 'Approve and send' })
      fireEvent.click(b); fireEvent.click(b)
      expect(onAction).toHaveBeenCalledTimes(1)
      expect(b.getAttribute('aria-disabled')).toBe('true')
      act(() => { vi.advanceTimersByTime(3100) })
      expect(b.getAttribute('aria-disabled')).toBeNull()
      fireEvent.click(b)
      expect(b.getAttribute('aria-disabled')).toBe('true')
      rerender(<A2UISurface surface={{ ...surface, lastSeq: 9 }} onAction={onAction} />)
      expect(screen.getByRole('button', { name: 'Approve and send' }).getAttribute('aria-disabled')).toBeNull()
    })
  })

  it('a resolved surface keeps its buttons disabled', () => {
    const onAction = vi.fn()
    render(<A2UISurface surface={{ ...fold(hilApproval), status: 'resolved' }} onAction={onAction} />)
    const b = screen.getByRole('button', { name: 'Approve and send' })
    expect(b.getAttribute('aria-disabled')).toBe('true')
    fireEvent.click(b)
    expect(onAction).not.toHaveBeenCalled()
  })

  it('renders hostile input as plain text and tolerates unknown types', () => {
    const big = 'x'.repeat(100000)
    const s = mk([
      { id: 'root', component: 'Column', children: ['a', 'b', 'c'] },
      { id: 'a', component: 'Text', text: '<script>window.__pwn=1</script><img src=x onerror=alert(1)>' },
      { id: 'b', component: 'Text', text: big },
      { id: 'c', component: 'Hologram' },
    ])
    const { container } = render(<A2UISurface surface={s} />)
    expect(container.querySelector('script, img')).toBeNull()
    expect(screen.getByText(/<script>window/)).toBeTruthy()
    expect(container.textContent.length).toBeLessThan(20000)
    expect(screen.getByTestId('a2ui-unknown')).toBeTruthy()
    expect(window.__pwn).toBeUndefined()
  })

  it('caps nodes at 200 and depth at 12, including cycles', () => {
    const many = [{ id: 'root', component: 'Column', children: Array.from({ length: 5000 }, (_, i) => `n${i}`) },
      ...Array.from({ length: 5000 }, (_, i) => ({ id: `n${i}`, component: 'Text', text: 't' }))]
    const { container } = render(<A2UISurface surface={mk(many)} />)
    expect(container.querySelectorAll('p.a2ui-text')).toHaveLength(MAX_NODES - 1)
    expect(screen.getByText('Card truncated: too large to show in full.')).toBeTruthy()
    const cyc = planTree({ root: { id: 'root', component: 'Column', children: ['root'] } })
    expect(cyc.order).toEqual(['root'])
    const chain = Object.fromEntries(Array.from({ length: 40 }, (_, i) => [i ? `d${i}` : 'root', { component: 'Card', child: `d${i + 1}` }]))
    expect(planTree(chain)).toMatchObject({ truncated: true })
    expect(planTree(chain).order).toHaveLength(13)
  })

  it('an error inside a component shows the fallback card, not a crash', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const s = mk([{ id: 'root', component: 'Row', children: 'oops', get justify() { throw new Error('boom') } }])
    render(<A2UISurface surface={s} />)
    expect(screen.getByText('This card could not be displayed.')).toBeTruthy()
    vi.restoreAllMocks()
  })
})
