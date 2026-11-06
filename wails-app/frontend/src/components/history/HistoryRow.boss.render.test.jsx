// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen } from '@testing-library/react'
import '../../i18n.js'
import HistoryRow from './HistoryRow.jsx'

afterEach(cleanup)
const row = { kind: 'chat', id: 'c1', title: 'Boss', updatedAt: '2026-10-25T10:00:00Z' }
const mount = r => render(<ul><HistoryRow row={r} live={{ status: 'idle', unread: 0 }} selected={false} onToggle={() => {}} onOpen={() => {}} /></ul>)

describe('history row boss mark', () => {
  it('marks the conversation of a run\'s boss, and only that one', () => {
    mount({ ...row, boss: true })
    expect(screen.getByTestId('hist-boss')).toHaveTextContent('Boss of an open-ended run')
    cleanup()
    mount(row)
    expect(screen.queryByTestId('hist-boss')).toBeNull()
  })
})
