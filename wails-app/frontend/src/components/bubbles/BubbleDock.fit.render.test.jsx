// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import '../../i18n.js'
import { BubbleDock } from './BubbleDock.jsx'
import { orbitOf } from '../../lib/sessionOrbit.js'
import { emptySummary } from '../../lib/coderBubbles.js'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

// Four missions, each with two agents, and a plain chat: the dock of the story in the final UI pass.
const S = (id, parent, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `conv-${id}`, title: `Agent ${id}`, state: 'running', counts: {}, ...extra })
const sessions = [1, 2, 3, 4].flatMap(m => [S(m, 0, { conversation_id: `m${m}`, title: `Mission ${m}` }), S(m * 10, m), S(m * 10 + 1, m, { state: 'done' })])
const bubbles = [...[1, 2, 3, 4].map(m => ({ key: `m${m}`, conversationId: `m${m}`, title: `Mission ${m}` })), { key: 'plain', conversationId: 'plain', title: 'Helper' }]
const store = { bubbles, expandedKey: '', side: 'right', summaryOf: () => emptySummary(), openDraft: vi.fn(), setSide: vi.fn(), reorder: vi.fn() }
const dock = () => render(<BubbleDock store={store} onOpen={vi.fn()} onClose={vi.fn()} registerEl={() => {}} titleOf={() => ''} orbitOf={b => orbitOf(sessions, { conversationId: b.conversationId })} />)
const setHeight = h => { Object.defineProperty(window, 'innerHeight', { configurable: true, value: h }) }

describe('BubbleDock in a window too short for every orbit', () => {
  it('draws every orbit when the window is tall enough', () => {
    setHeight(1800)
    const { container } = dock()
    expect(container.querySelectorAll('[data-testid="orbit"]')).toHaveLength(4)
    expect(container.querySelector('[data-testid="bubble-overflow"]')).toBeNull()
  })

  it('at 800px keeps the "+" and the person tray on screen by dropping the later circles first', () => {
    setHeight(800)
    const { container } = dock()
    const orbits = container.querySelectorAll('[data-testid="orbit"]').length
    expect(orbits).toBeGreaterThan(0)
    expect(orbits).toBeLessThan(4)
    expect(screen.getByRole('button', { name: /New coder chat|new/i })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /^Mission \d/ }).length + screen.getAllByRole('button', { name: /^Helper/ }).length).toBe(5)
    // The first mission keeps its circles.
    expect(container.querySelector('.bubble-wrap.has-orbit [data-bubble="m1"]')).not.toBeNull()
  })

  it('at 500px puts bubbles that do not fit behind the "+N" bubble, and refits when the window is resized', () => {
    setHeight(500)
    const { container } = dock()
    expect(screen.getByRole('button', { name: /more/i })).toBeInTheDocument()
    expect(container.querySelectorAll('[data-bubble]').length).toBeLessThan(5)
    act(() => { setHeight(1800); window.dispatchEvent(new Event('resize')) })
    expect(container.querySelectorAll('[data-testid="orbit"]')).toHaveLength(4)
    expect(screen.queryByRole('button', { name: /more/i })).toBeNull()
  })
})
