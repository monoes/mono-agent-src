// @vitest-environment jsdom
import React, { useState } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, configure } from '@testing-library/react'
import '../../i18n.js'

configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })

const { api } = vi.hoisted(() => ({
  api: { steerBoss: vi.fn(), listHilItems: vi.fn(() => Promise.resolve([])), getHilAsks: vi.fn(() => Promise.resolve([])) },
}))
vi.mock('../../services/api.js', () => ({
  api: new Proxy(api, { get: (t, k) => (k in t ? t[k] : () => Promise.resolve([])) }),
  notify: vi.fn(),
  onChatEvent: () => () => {},
}))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: { list: () => Promise.resolve({ sessions: [] }) } }))
vi.mock('./useHilAsks.js', () => ({ useHilAsks: () => ({ asks: [], busy: {}, outcome: {}, resolve: vi.fn() }) }))
vi.mock('./PersonItemViews.jsx', () => ({ PersonItemsRegion: () => null }))
vi.mock('./mission/MissionStrip.jsx', () => ({ default: () => null }))

import { CoderChatView } from './CoderChatView.jsx'

const conv = (send) => ({
  conversationId: 'chat-1', messages: [], liveTurn: { calls: {}, lastSeq: 0 }, activeTurnId: '', streaming: false, loading: false,
  loadError: '', announcement: '', send, stop: vi.fn(), model: '',
})

function Harness({ send, steering, direct = false }) {
  const [draft, setDraft] = useState('')
  return <><span data-testid="draft">{draft}</span><CoderChatView conv={conv(send)} isDraft={false} access="full" setup={{ workspace: { kind: "root" }, runtime: "", model: "", effort: "" }} onSetupChange={() => {}} draft={draft} onDraftChange={setDraft} steerChat={steering ? 'chat-1' : ''} directBoss={direct} /></>
}

beforeEach(() => { Element.prototype.scrollTo = function scrollTo() {} })
afterEach(() => { cleanup(); vi.clearAllMocks() })

const type = (text) => {
  const box = screen.getByRole('textbox')
  fireEvent.change(box, { target: { value: text } })
  fireEvent.keyDown(box, { key: 'Enter' })
}

describe('typing in a chat whose open-ended run is going', () => {
  it('sends the text as steering to the boss and never as a message to the chat', async () => {
    api.steerBoss.mockResolvedValue({ commandId: 1, conversationId: 'boss-conv', turnId: 't-1' })
    const send = vi.fn()
    render(<Harness send={send} steering />)
    expect(screen.getByTestId('steering-bar')).toHaveTextContent('Steering: you are talking to the boss, not approving anything')
    type('prefer the smaller change')
    await waitFor(() => expect(api.steerBoss).toHaveBeenCalledWith('chat-1', 'prefer the smaller change'))
    expect(send).not.toHaveBeenCalled()
    await waitFor(() => expect(screen.getByTestId('steering-bar')).toHaveTextContent('Sent to the boss: prefer the smaller change'))
    expect(screen.getByTestId('draft')).toHaveTextContent(/^$/)
  })
  it('keeps the text and says why when the CLI refuses it', async () => {
    api.steerBoss.mockResolvedValue({ error: 'no boss is running behind chat chat-1', code: 'invalid_input' })
    render(<Harness send={vi.fn()} steering />)
    type('hello')
    await waitFor(() => expect(screen.getByTestId('steering-bar')).toHaveTextContent('Not sent: no boss is running behind chat chat-1'))
    expect(screen.getByTestId('draft')).toHaveTextContent('hello')
  })
  it('is an ordinary chat when no run is going: no bar, a normal send', async () => {
    const send = vi.fn(() => Promise.resolve(true))
    render(<Harness send={send} steering={false} />)
    expect(screen.queryByTestId('steering-bar')).toBeNull()
    type('hello')
    await waitFor(() => expect(send).toHaveBeenCalled())
    expect(api.steerBoss).not.toHaveBeenCalled()
  })
})
