// @vitest-environment jsdom
// W5 R21-28: filtering must not remount the rows that stay visible.
import { describe, it, expect, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../i18n.js'
import Logs from './Logs.jsx'

afterEach(cleanup)

describe('Logs row keys', () => {
  it('keeps the DOM node of a row when a filter hides rows above it', () => {
    const logs = [
      { time: '10:00', source: 'a', level: 'INFO', message: 'first line' },
      { time: '10:01', source: 'b', level: 'ERROR', message: 'second line' },
    ]
    window.HTMLElement.prototype.scrollIntoView = () => {}
    render(<Logs logs={logs} onClear={() => {}} onRefresh={() => {}} />)
    const before = screen.getByText('second line')
    const filter = screen.getByRole('textbox')
    fireEvent.change(filter, { target: { value: 'second' } })
    expect(screen.queryByText('first line')).not.toBeInTheDocument()
    expect(screen.getByText('second line')).toBe(before)
  })
})
