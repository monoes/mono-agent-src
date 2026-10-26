// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render } from '@testing-library/react'
import '../../i18n.js'
import SessionSwitcher from './SessionSwitcher.jsx'

const sessions = [1, 2, 3, 4].map(id => ({ id, title: `Mission ${id}`, state: 'running', counts: { done: 0, total: 1 }, budget_usd: 0, spent_usd: 0 }))
afterEach(() => { cleanup(); delete Element.prototype.scrollIntoView })

describe('SessionSwitcher', () => {
  it('brings the selected tab into view when a link (not a click on it) selects a session', () => {
    const seen = []
    Element.prototype.scrollIntoView = function scrollIntoView() { seen.push(this.textContent) }
    const { rerender } = render(<SessionSwitcher sessions={sessions} selectedId={null} onSelect={() => {}} />)
    rerender(<SessionSwitcher sessions={sessions} selectedId={4} onSelect={() => {}} />)
    expect(seen.at(-1)).toContain('Mission 4')
  })
})
