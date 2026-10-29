// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'

const { sessionsApi } = vi.hoisted(() => ({ sessionsApi: { plan: vi.fn() } }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi }))
import PlanView from './PlanView.jsx'

const task = (id, extra = {}) => ({ id, title: `Plan task ${id}`, status: 'inbox', ...extra })

beforeEach(() => sessionsApi.plan.mockReset())
afterEach(cleanup)

const rows = () => document.querySelectorAll('.ts-plan-row')

describe('a long plan renders a bounded number of rows', () => {
  it('shows the first 100 rows of a flat 1000-task plan, then more on request', async () => {
    sessionsApi.plan.mockResolvedValue({ tasks: Array.from({ length: 1000 }, (_, i) => task(i + 1)) })
    render(<PlanView sessionId={1} rev={1} onOpen={() => {}} />)
    await screen.findByText('Plan task 1')
    expect(rows().length).toBe(100)
    fireEvent.click(screen.getByRole('button', { name: /Show 100 more rows/ }))
    expect(rows().length).toBe(200)
  })

  it('counts nested rows too: one epic with 1000 children is also bounded', async () => {
    sessionsApi.plan.mockResolvedValue({ tasks: [task(1, { kind: 'epic' }), ...Array.from({ length: 1000 }, (_, i) => task(i + 2, { parent_id: 1 }))] })
    render(<PlanView sessionId={1} rev={1} onOpen={() => {}} />)
    await screen.findByText('Plan task 1')
    await waitFor(() => expect(rows().length).toBe(100))
    expect(screen.getByRole('button', { name: /Show 100 more rows/ })).toBeInTheDocument()
  })

  it('a plan within the bound shows every row and no control', async () => {
    sessionsApi.plan.mockResolvedValue({ tasks: Array.from({ length: 100 }, (_, i) => task(i + 1)) })
    render(<PlanView sessionId={1} rev={1} onOpen={() => {}} />)
    await screen.findByText('Plan task 100')
    expect(rows().length).toBe(100)
    expect(screen.queryByRole('button', { name: /Show .* more/ })).not.toBeInTheDocument()
  })
})
