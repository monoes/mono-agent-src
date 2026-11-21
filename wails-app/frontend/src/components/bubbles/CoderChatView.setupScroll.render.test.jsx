// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({
  api: new Proxy({}, { get: () => () => Promise.resolve([]) }),
  notify: vi.fn(),
  onChatEvent: () => () => {},
}))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: { list: () => Promise.resolve({ sessions: [] }) } }))
vi.mock('./useHilAsks.js', () => ({ useHilAsks: () => ({ asks: [], busy: {}, outcome: {}, resolve: vi.fn() }) }))
vi.mock('./PersonItemViews.jsx', () => ({ PersonItemsRegion: () => null }))
vi.mock('./mission/MissionStrip.jsx', () => ({ default: () => null }))
vi.mock('./useChatSetup.js', () => ({ useChatSetup: () => ({}) }))
vi.mock('./ChatSetup.jsx', () => ({
  ChatSetup: () => <div data-testid="tall-setup" style={{ height: 4000 }} />,
  BlockedReason: () => null,
}))

import { CoderChatView } from './CoderChatView.jsx'

const conv = {
  conversationId: '', messages: [], liveTurn: { calls: {}, lastSeq: 0 }, activeTurnId: '', streaming: false, loading: false,
  loadError: '', announcement: '', send: vi.fn(), stop: vi.fn(), model: '',
}

afterEach(cleanup)

describe('the setup form of a new chat', () => {
  it('scrolls inside its own box and can shrink, so the composer stays reachable', () => {
    Element.prototype.scrollTo = function scrollTo() {}
    render(<CoderChatView conv={conv} isDraft access="full" setup={{ workspace: { kind: 'root' }, runtime: '', model: '', effort: '' }}
      onSetupChange={() => {}} draft="" onDraftChange={() => {}} />)
    const box = screen.getByTestId('chat-setup-scroll')
    expect(box).toContainElement(screen.getByTestId('tall-setup'))
    expect(box.style.overflowY).toBe('auto')
    expect(box.style.minHeight).toBe('0px')
    expect(box.style.flex).toMatch(/^0 1 auto/)
    expect(screen.getByRole('textbox')).toBeInTheDocument()
  })
})
