// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../../i18n.js'
import { OrbitRing } from './OrbitRing.jsx'
import { orbitOf } from '../../lib/sessionOrbit.js'

afterEach(cleanup)

const S = (id, parent, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `Agent ${id}`, state: 'running', counts: {}, ...extra })
const orbit = (kids, extra = []) => orbitOf([S(1, 0), ...kids, ...extra], { sessionId: 1 })
const buttons = () => screen.getAllByRole('button')

describe('OrbitRing', () => {
  it('renders nothing for a session with no agents of its own', () => {
    const { container } = render(<OrbitRing orbit={orbit([])} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('draws one button per agent, named by title and state (never colour alone), with a link under each', () => {
    const o = orbit([S(2, 1), S(3, 1, { state: 'draft' }), S(4, 1, { state: 'done' }), S(5, 1, { state: 'failed' })])
    const { container } = render(<OrbitRing orbit={o} />)
    expect(screen.getByRole('button', { name: 'Agent 3, Waiting for you' })).toHaveAttribute('data-state', 'waiting')
    expect(screen.getByRole('button', { name: 'Agent 4, Done' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Agent 5, Failed' })).toBeInTheDocument()
    expect(buttons()).toHaveLength(4)
    expect(container.querySelectorAll('svg.orbit-links path')).toHaveLength(4)
    expect(container.querySelector('svg.orbit-links')).toHaveAttribute('aria-hidden', 'true')
    // The state is also an icon in the circle, apart from the ring colour.
    expect(container.querySelectorAll('.orbit-mark svg')).toHaveLength(4)
  })

  it('gives EVERY button of the ring, the overflow one included, a non-empty aria-label', () => {
    const kids = Array.from({ length: 12 }, (_, i) => S(i + 2, 1))
    const { container } = render(<OrbitRing orbit={orbit(kids)} />)
    const all = [...container.querySelectorAll('button')]
    expect(all.length).toBeGreaterThan(8)
    for (const b of all) expect((b.getAttribute('aria-label') || '').trim()).not.toBe('')
  })

  it('shows a stopped agent as its own neutral state: label, icon and ring, not as failed', () => {
    const { container } = render(<OrbitRing orbit={orbit([S(2, 1, { state: 'stopped' }), S(3, 1, { state: 'failed' })])} />)
    const stopped = screen.getByRole('button', { name: 'Agent 2, Stopped' })
    expect(stopped).toHaveAttribute('data-state', 'stopped')
    expect(screen.getByRole('button', { name: 'Agent 3, Failed' })).toHaveAttribute('data-state', 'failed')
    expect(stopped.querySelector('.orbit-mark svg')).not.toBeNull()
    expect(container.querySelector('path.orbit-link.stopped')).not.toBeNull()
    expect(stopped).toHaveTextContent('Stopped')
  })

  it('says in the tooltip and the name that a circle without a conversation opens its task board', () => {
    const o = orbitOf([S(1), S(2, 1, { conversation_id: '', title: 'Crawl' }), S(3, 1, { title: 'Chatty' })], { sessionId: 1 })
    render(<OrbitRing orbit={o} />)
    const bare = screen.getByRole('button', { name: /^Crawl, Running, No conversation yet: opens its task board$/ })
    expect(bare).toHaveTextContent('No conversation yet: opens its task board')
    expect(screen.getByRole('button', { name: 'Chatty, Running' })).not.toHaveTextContent('opens its task board')
  })

  it('opens the clicked agent', () => {
    const onOpen = vi.fn()
    render(<OrbitRing orbit={orbit([S(2, 1), S(3, 1)])} onOpen={onOpen} />)
    fireEvent.click(screen.getByRole('button', { name: /Agent 3/ }))
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: 3, conversationId: 'c3' }))
  })

  it('shows a count on an agent that has agents of its own, and says so in its name', () => {
    const o = orbit([S(2, 1)], [S(3, 2), S(4, 2)])
    render(<OrbitRing orbit={o} />)
    const b = screen.getByRole('button', { name: 'Agent 2, Running, 2 agents of its own' })
    expect(b.querySelector('.orbit-count')).toHaveTextContent('2')
  })

  it('moves between agents with Left and Right, wrapping, one tab stop', () => {
    render(<OrbitRing orbit={orbit([S(2, 1), S(3, 1), S(4, 1)])} />)
    const [a, b, c] = buttons()
    expect([a, b, c].map(x => x.tabIndex)).toEqual([0, -1, -1])
    a.focus()
    fireEvent.keyDown(a, { key: 'ArrowRight' })
    expect(b).toHaveFocus()
    expect(b.tabIndex).toBe(0)
    fireEvent.keyDown(b, { key: 'ArrowLeft' })
    fireEvent.keyDown(a, { key: 'ArrowLeft' })
    expect(c).toHaveFocus()
    fireEvent.keyDown(c, { key: 'ArrowRight' })
    expect(a).toHaveFocus()
  })

  it('shows eight agents and a +N chip for the rest, which opens a list', () => {
    const kids = Array.from({ length: 10 }, (_, i) => S(10 + i, 1))
    const onOpen = vi.fn()
    render(<OrbitRing orbit={orbit(kids)} onOpen={onOpen} />)
    expect(screen.getAllByRole('button', { name: /^Agent \d+, Running$/ })).toHaveLength(8)
    const chip = screen.getByRole('button', { name: '2 more agents' })
    expect(chip).toHaveTextContent('+2')
    expect(chip).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(chip)
    expect(chip).toHaveAttribute('aria-expanded', 'true')
    const list = screen.getByRole('list', { name: 'More agents' })
    fireEvent.click(list.querySelectorAll('button')[1])
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: 19 }))
    expect(screen.queryByRole('list')).toBeNull()
  })

  it('Escape closes the +N list and gives focus back to the chip', () => {
    const kids = Array.from({ length: 9 }, (_, i) => S(10 + i, 1))
    render(<OrbitRing orbit={orbit(kids)} />)
    const chip = screen.getByRole('button', { name: '1 more agent' })
    fireEvent.click(chip)
    fireEvent.keyDown(screen.getByRole('list').querySelector('button'), { key: 'Escape' })
    expect(screen.queryByRole('list')).toBeNull()
    expect(chip).toHaveFocus()
  })

  it('lists waiting agents before the chip can hide them', () => {
    const kids = Array.from({ length: 9 }, (_, i) => S(10 + i, 1))
    render(<OrbitRing orbit={orbit(kids, [S(99, 1, { state: 'draft' })])} />)
    expect(screen.getByRole('button', { name: 'Agent 99, Waiting for you' })).toBeInTheDocument()
  })

  it('puts the tooltip text in the DOM for hover and focus, hidden from screen readers', () => {
    render(<OrbitRing orbit={orbit([S(2, 1, { spent_usd: 1.5, counts: { done: 2, total: 5 } })])} />)
    const tip = document.querySelector('.orbit-tip')
    expect(tip).toHaveAttribute('aria-hidden', 'true')
    expect(tip).toHaveTextContent('Agent 2')
    expect(tip).toHaveTextContent('Running · 2/5 tasks · $1.50')
  })
})
