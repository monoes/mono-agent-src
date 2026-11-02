// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { CoderUsageBar, sessionUsage, contextWindowFor } from './CoderHeader.jsx'

afterEach(cleanup)

const turn = (peak, input, output) => ({ coderUsage: { peakContextTokens: peak, models: { m: { input, output, cacheRead: 10, cacheCreation: 5 } } } })

describe('CoderUsageBar', () => {
  it('shows the latest context and the session total across turns', () => {
    const states = [turn(50000, 100, 200), turn(100000, 300, 400), { coderUsage: null }]
    expect(sessionUsage(states).sum).toBe(100 + 200 + 300 + 400 + 30)
    render(<CoderUsageBar states={states} model="claude-opus" />)
    expect(screen.getByTestId('coder-usage-context').textContent).toBe('100.0k / 200.0k · 50%')
    expect(screen.getByTestId('coder-usage-total').textContent).toBe('Session 1.0k tokens')
  })
  it('renders nothing before any usage', () => {
    const { container } = render(<CoderUsageBar states={[{}]} model="" />)
    expect(container.firstChild).toBeNull()
  })
  it('uses a 1M window for 1m models', () => {
    expect(contextWindowFor('claude-opus-4-7[1m]')).toBe(1_000_000)
    expect(contextWindowFor('claude-opus')).toBe(200_000)
  })
  it('keeps the bar visible on a narrow pane: the row wraps and the track has a minimum width', () => {
    render(<CoderUsageBar states={[turn(50000, 100, 200)]} model="claude-opus" />)
    const bar = screen.getByRole('progressbar')
    expect(bar.style.minWidth).toBe('60px')
    expect(screen.getByTestId('coder-usage').style.flexWrap).toBe('wrap')
  })
})
