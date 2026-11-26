// @vitest-environment jsdom
// #205 item 6: a full-access role's native tool calls reach the org bus as
// tool_activity events and show as the coder chat's cards, per role.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, within } from '@testing-library/react'

vi.mock('../../services/api.js', () => ({ api: { openURL: vi.fn(), coderStopBackground: vi.fn() } }))

import OrgToolActivity from './OrgToolActivity.jsx'
import { buildToolActivity, isToolActivity } from './orgToolActivity.js'
import '../../i18n.js'

afterEach(() => { cleanup() })

const T0 = Date.UTC(2026, 8, 28, 10, 0, 0)
const ta = (from, id, phase, rest = {}, dt = 0) => ({ v: 1, type: 'tool_activity', from, ts: T0 + dt, id, phase, ...rest })
const events = [
  { v: 1, type: 'message', from: 'growth:lead', to: 'growth:builder', subject: 'ship it', ts: T0 },
  ta('growth:builder', 't1', 'start', { name: 'Bash', input: { command: 'npm test', description: 'Run tests' } }, 1000),
  ta('growth:builder', 't1', 'end', { name: 'Bash', ok: true, output: 'all green', duration_ms: 812 }, 2000),
  ta('growth:builder', 'task', 'start', { name: 'Task', input: { description: 'Survey', prompt: 'look', subagent_type: 'Explore' } }, 3000),
  ta('growth:builder', 'g1', 'start', { name: 'Grep', input: { pattern: 'TODO' }, parent_tool_use_id: 'task' }, 3100),
  ta('growth:builder', 'g1', 'end', { name: 'Grep', ok: true, output: 'a.go:1' }, 3200),
  ta('growth:ops', 'e1', 'start', { name: 'Edit', input: { file_path: '/w/deploy.sh', old_string: 'a', new_string: 'b' } }, 4000),
  ta('', 'e1', 'end', { name: 'Edit', ok: false, denied: true, output: 'no', output_truncated: true }, 4100),
  ta('growth:ops', 'b9', 'start', { name: 'Bash', input: { command: 'sleep 60' } }, 5000),
]

describe('buildToolActivity', () => {
  it('pairs start/end by id into one state per role, keeping nesting and flags', () => {
    const groups = buildToolActivity(events)
    expect(groups.map(g => g.role)).toEqual(['builder', 'ops'])
    const [builder, ops] = groups
    expect(builder.state.parts.map(p => p.callId)).toEqual(['t1', 'task', 'g1'])
    expect(builder.state.calls.t1).toMatchObject({ name: 'Bash', native: true, status: 'completed', ok: true, result: 'all green', durationMs: 812, arguments: { command: 'npm test', description: 'Run tests' } })
    expect(builder.state.calls.g1.parentCallId).toBe('task')
    // An end with no from still lands on the role that started the call.
    expect(ops.state.calls.e1).toMatchObject({ status: 'completed', ok: false, denied: true, truncated: true })
    expect(ops.state.calls.b9.status).toBe('started')
    expect(isToolActivity(events[0])).toBe(false)
    expect(buildToolActivity([events[0]])).toEqual([])
  })
})

describe('OrgToolActivity', () => {
  it('renders each role\'s calls with the native tool cards, subagent calls nested', () => {
    render(<OrgToolActivity events={events} runKey="r1" />)
    const [builder, ops] = screen.getAllByTestId('org-tool-activity-role')
    expect(builder).toHaveAttribute('data-role', 'builder')
    expect(builder).toHaveTextContent('builder· 3 tool calls')
    const cards = within(builder).getAllByTestId('native-tool-card')
    expect(cards.map(c => c.dataset.tool)).toEqual(['Bash', 'Task', 'Grep'])
    expect(within(cards[0]).getByText('$ npm test')).toBeInTheDocument()
    expect(within(cards[0]).getByText('· 812ms')).toBeInTheDocument()
    expect(within(within(cards[1]).getByTestId('subagent-calls')).getByText('Grep TODO')).toBeInTheDocument()
    const opsCards = within(ops).getAllByTestId('native-tool-card')
    expect(opsCards[0]).toHaveAttribute('data-status', 'denied')
    expect(within(opsCards[0]).getByTestId('truncated')).toBeInTheDocument()
    // A finished run's call without an end reads interrupted, not running.
    expect(opsCards[1]).toHaveAttribute('data-status', 'interrupted')
  })

  it('a live run keeps an open call running', () => {
    render(<OrgToolActivity events={events} isLive runKey="live" />)
    const ops = screen.getAllByTestId('org-tool-activity-role')[1]
    expect(within(ops).getAllByTestId('native-tool-card')[1]).toHaveAttribute('data-status', 'running')
  })

  it('renders nothing without tool_activity', () => {
    const { container } = render(<OrgToolActivity events={[events[0]]} />)
    expect(container).toBeEmptyDOMElement()
  })
})
