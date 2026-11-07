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

describe('the boss in the orbit ring', () => {
  it('is a distinct, named satellite that opens its own conversation', () => {
    const boss = S(9, 0, { title: 'Boss', boss: true, client_id: 'boss-for-c1-1' })
    const o = orbitOf([S(1, 0), S(2, 1), boss], { conversationId: 'c1' })
    const onOpen = vi.fn()
    render(<OrbitRing orbit={o} onOpen={onOpen} />)
    const b = screen.getByRole('button', { name: 'Boss, Running, Boss' })
    expect(b).toHaveAttribute('data-boss', 'true')
    expect(b).toHaveTextContent('Boss of the open-ended run: click to open its conversation')
    expect(screen.getByRole('button', { name: 'Agent 2, Running' })).not.toHaveAttribute('data-boss')
    fireEvent.click(b)
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: 9, conversationId: 'c9', boss: true }))
  })
})
