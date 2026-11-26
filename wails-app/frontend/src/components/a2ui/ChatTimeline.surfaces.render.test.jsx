// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { ChatTimeline } from '../chat/ChatTimeline.jsx'
import { chatReducer, initialChatState } from '../chat/chatReducer.js'
import { hilApproval } from './fixtures.js'
import '../../i18n.js'

afterEach(cleanup)
const ev = (seq, type, payload) => ({ type: 'event', event: { conversationId: 'c', turnId: 't', seq, at: '2026-11-24T10:00:00Z', type, payload } })

describe('ChatTimeline surfaces', () => {
  it('places a surface between the parts it arrived between and forwards the action', () => {
    let s = chatReducer(initialChatState(), { type: 'scope', scope: { conversationId: 'c', turnId: 't' } })
    s = chatReducer(s, ev(1, 'assistant.delta', { partId: 'p1', text: 'before card' }))
    s = chatReducer(s, ev(2, 'ui.surface', hilApproval[0]))
    s = chatReducer(s, ev(3, 'assistant.delta', { partId: 'p2', text: 'after card' }))
    const onA2UIAction = vi.fn()
    render(<ChatTimeline state={s} onA2UIAction={onA2UIAction} />)
    const html = screen.getByTestId('chat-timeline').textContent
    expect(html.indexOf('before card')).toBeLessThan(html.indexOf('from monoagent'))
    expect(html.indexOf('from monoagent')).toBeLessThan(html.indexOf('after card'))
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    expect(onA2UIAction.mock.calls[0][0].action.name).toBe('hil.reject')
  })
  it('renders a surface even with no parts', () => {
    let s = chatReducer(initialChatState(), { type: 'scope', scope: { conversationId: 'c', turnId: 't' } })
    s = chatReducer(s, ev(1, 'ui.surface', hilApproval[0]))
    render(<ChatTimeline state={s} />)
    expect(screen.getByTestId('a2ui-surface')).toBeTruthy()
  })
  // jsdom lays nothing out: this holds the two rules that give a card its width.
  it('lets a card take the width of the chat, up to a readable maximum', () => {
    let s = chatReducer(initialChatState(), { type: 'scope', scope: { conversationId: 'c', turnId: 't' } })
    s = chatReducer(s, ev(1, 'assistant.delta', { partId: 'p1', text: 'text' }))
    const { rerender } = render(<ChatTimeline state={s} />)
    expect(screen.getByTestId('chat-timeline').style.alignSelf).toBe('')
    rerender(<ChatTimeline state={chatReducer(s, ev(2, 'ui.surface', hilApproval[0]))} />)
    expect(screen.getByTestId('chat-timeline').style.alignSelf).toBe('stretch')
    const css = readFileSync(join(__dirname, 'a2ui.css'), 'utf8')
    const card = /^\.a2ui-surface \{([^}]*)\}/m.exec(css)[1]
    for (const rule of ['box-sizing: border-box', 'width: 100%', 'max-width: 640px']) expect(card).toContain(rule)
  })
})
