// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'

afterEach(cleanup)
const md = (o) => `\`\`\`chart\n${JSON.stringify(o)}\n\`\`\``
const bar = { type: 'bar', title: 'Runs per day', unit: 'runs', x: ['Mon', 'Tue', 'Wed'], series: [{ name: 'ok', data: [4, 6, 5] }, { name: 'failed', data: [1, 0, 2] }] }

describe('ChartBlock', () => {
  it('renders a bar chart with a legend, and a table toggle with the same data', () => {
    const { container } = render(<ChatMarkdown content={md(bar)} />)
    expect(screen.getByTestId('chat-block-chart')).toBeInTheDocument()
    expect(container.querySelectorAll('svg[height="190"] path').length).toBe(6)
    expect(screen.getByText('failed')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /table/i }))
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'ok' })).toBeInTheDocument()
    expect(screen.getByRole('rowheader', { name: 'Tue' })).toBeInTheDocument()
    expect(screen.getByText('6 runs')).toBeInTheDocument()
  })
  it('shows a tooltip on keyboard focus and moves with arrow keys', () => {
    render(<ChatMarkdown content={md(bar)} />)
    const g = screen.getByRole('group', { name: 'Runs per day' })
    fireEvent.focus(g)
    expect(screen.getByTestId('chart-tooltip')).toHaveTextContent('Mon')
    fireEvent.keyDown(g, { key: 'ArrowRight' })
    expect(screen.getByTestId('chart-tooltip')).toHaveTextContent('Tue')
    expect(screen.getByTestId('chart-tooltip')).toHaveTextContent('6 runs')
    fireEvent.keyDown(g, { key: 'Escape' })
    expect(screen.queryByTestId('chart-tooltip')).toBeNull()
  })
  it('draws line and area charts', () => {
    for (const type of ['line', 'area']) {
      const { container, unmount } = render(<ChatMarkdown content={md({ ...bar, type })} />)
      expect(container.querySelectorAll('polyline').length).toBe(2)
      unmount()
    }
  })
  it('renders a donut with focusable slices and a tooltip', () => {
    const { container } = render(<ChatMarkdown content={md({ type: 'donut', title: 'Share', slices: [{ label: 'a', value: 3 }, { label: 'b', value: 1 }] })} />)
    const slices = container.querySelectorAll('path[tabindex="0"]')
    expect(slices.length).toBe(2)
    fireEvent.focus(slices[0])
    expect(screen.getByTestId('chart-tooltip')).toHaveTextContent('75%')
  })
  it('renders a sparkline and stat tiles', () => {
    render(<ChatMarkdown content={md({ type: 'sparkline', title: 'Latency', unit: 'ms', data: [3, 5, 4] })} />)
    expect(screen.getByRole('img', { name: /Latency\. 4 ms latest/ })).toBeInTheDocument()
    cleanup()
    render(<ChatMarkdown content={md({ type: 'stat', stats: [{ label: 'Runs', value: 1284, delta: '+3%', trend: 'up' }, { label: 'Name', value: 'ok' }] })} />)
    expect(screen.getByRole('group', { name: 'Runs: 1284, +3%' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /table/i })).toBeNull()
  })
  it('falls back to the code block for an invalid chart', () => {
    render(<ChatMarkdown content={md({ type: 'bar', x: ['a'], series: [{ name: 's', data: [1, 2] }] })} />)
    expect(screen.queryByTestId('chat-block-chart')).toBeNull()
  })
  it('survives constant, negative and single-point data', () => {
    for (const o of [
      { type: 'line', x: ['a', 'b'], series: [{ name: 's', data: [2, 2] }] },
      { type: 'bar', x: ['a', 'b'], series: [{ name: 's', data: [-3, 4] }] },
      { type: 'area', x: ['a'], series: [{ name: 's', data: [0] }] },
    ]) {
      const { unmount } = render(<ChatMarkdown content={md(o)} />)
      expect(screen.getByTestId('chat-block-chart')).toBeInTheDocument()
      unmount()
    }
  })
})
