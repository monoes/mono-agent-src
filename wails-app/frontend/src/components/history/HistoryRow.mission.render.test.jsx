// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen } from '@testing-library/react'
import '../../i18n.js'
import HistoryRow from './HistoryRow.jsx'

afterEach(cleanup)
const row = { kind: 'chat', id: 'c1', title: 'Export', updatedAt: '2026-10-25T10:00:00Z' }
const live = { status: 'idle', unread: 0 }
const mount = (mission, r = row) => render(<ul><HistoryRow row={r} live={live} mission={mission} selected={false} onToggle={() => {}} onOpen={() => {}} /></ul>)

describe('history row mission badge', () => {
  it('shows the state of a conversation\'s mission', () => {
    mount('running')
    expect(screen.getByTestId('hist-mission')).toHaveTextContent('Mission: Running')
  })
  it('shows nothing for a chat without one', () => {
    mount(undefined)
    expect(screen.queryByTestId('hist-mission')).toBeNull()
  })
  it('shows the chat mode of a mission or open-ended chat, and nothing for an off one', () => {
    mount(undefined, { ...row, chatMode: 'mission' })
    expect(screen.getByTestId('hist-chat-mode')).toHaveTextContent('Mode: Mission')
    cleanup()
    mount(undefined, { ...row, chatMode: 'off' })
    expect(screen.queryByTestId('hist-chat-mode')).toBeNull()
  })
})
