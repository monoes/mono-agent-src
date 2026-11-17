// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, configure } from '@testing-library/react'
import '../../i18n.js'

configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })

vi.mock('../../services/api.js', () => ({
  api: new Proxy({}, { get: () => () => Promise.resolve([]) }),
  notify: vi.fn(),
  onChatEvent: () => () => {},
}))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: { list: () => Promise.resolve({ sessions: [] }) } }))
vi.mock('./useHilAsks.js', () => ({ useHilAsks: () => ({ asks: [], busy: {}, outcome: {}, resolve: vi.fn() }) }))
vi.mock('./PersonItemViews.jsx', () => ({ PersonItemsRegion: () => null }))
vi.mock('./mission/MissionStrip.jsx', () => ({ default: () => null }))
vi.mock('./useCoderConversation.js', () => ({
  useCoderConversation: ({ conversationId }) => ({
    conversationId, messages: [], liveTurn: { calls: {}, lastSeq: 0 }, activeTurnId: '', streaming: false, loading: false,
    loadError: '', announcement: '', send: vi.fn(), stop: vi.fn(), model: '',
  }),
}))

import { RoleChat } from './RoleChat.jsx'

const setup = { workspace: { kind: 'root' }, runtime: '', model: '', effort: '' }
beforeEach(() => { Element.prototype.scrollTo = function scrollTo() {} })
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('RoleChat', () => {
  it('opens a planner chat in place with a way back, as a plain lite chat', () => {
    const onBack = vi.fn()
    render(<RoleChat role={{ id: 5, conversationId: 'plan-1', boss: false }} frontDesk="desk-1" access="full" setup={setup} runtime="claude" onBack={onBack} />)
    expect(screen.getByTestId('role-chat')).toBeInTheDocument()
    expect(screen.queryByTestId('steering-bar')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /front desk/i }))
    expect(onBack).toHaveBeenCalled()
  })

  it('treats typing to the boss as steering', () => {
    render(<RoleChat role={{ id: 1, conversationId: 'boss-1', boss: true }} frontDesk="desk-1" access="full" setup={setup} runtime="claude" onBack={() => {}} />)
    expect(screen.getByTestId('steering-bar')).toBeInTheDocument()
  })
})
