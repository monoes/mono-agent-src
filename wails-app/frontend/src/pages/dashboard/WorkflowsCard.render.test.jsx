// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k, o) => (o?.count != null ? `${k}:${o.count}` : k) }) }))
import WorkflowsCard from './WorkflowsCard.jsx'
import RecentRunsCard from './RecentRunsCard.jsx'

afterEach(cleanup)
const wfs = [{ id: 'w1', name: 'Scraper', is_active: true }, { id: 'w2', name: 'Digest', is_active: true }]
const execs = [{ id: 'e1', workflow_id: 'w1', status: 'SUCCESS', created_at: new Date().toISOString() }]
const noop = { onRun: vi.fn(), onStop: vi.fn(), onToggle: vi.fn(), onNavigate: vi.fn() }

describe('WorkflowsCard', () => {
  it('shows next run for scheduled workflows and paused when the daemon is off', () => {
    const next = new Date(Date.now() + 20 * 60000).toISOString()
    const { rerender } = render(<WorkflowsCard workflows={wfs} executions={execs} {...noop}
      schedules={{ daemon_running: true, upcoming: [{ workflow_id: 'w1', next_run: next }], invalid: [] }} />)
    expect(screen.getByText('dashboard.time.inMinutes:20')).toBeInTheDocument()
    // jsdom 31 resolves var() in computed style, so assert the authored inline value
    expect(screen.getByText('SUCCESS').getAttribute('style')).toContain('color: var(--green-neon)')
    rerender(<WorkflowsCard workflows={wfs} executions={execs} {...noop}
      schedules={{ daemon_running: false, upcoming: [{ workflow_id: 'w1', next_run: next }], invalid: [] }} />)
    expect(screen.getByText('dashboard.workflows.schedulePaused')).toBeInTheDocument()
  })
  it('an @every schedule shows its interval', () => {
    render(<WorkflowsCard workflows={wfs} executions={[]} {...noop}
      schedules={{ daemon_running: true, upcoming: [{ workflow_id: 'w1', next_run: new Date().toISOString(), every: '5m', cron: '@every 5m' }], invalid: [] }} />)
    expect(screen.getByTitle('@every 5m')).toBeInTheDocument()
  })
  it('an @every schedule the daemon reported shows its real next run', () => {
    const next = new Date(Date.now() + 17 * 60000).toISOString()
    render(<WorkflowsCard workflows={wfs} executions={[]} {...noop}
      schedules={{ daemon_running: true, upcoming: [{ workflow_id: 'w1', next_run: next, every: '1h', cron: '@every 1h', source: 'daemon' }], invalid: [] }} />)
    expect(screen.getByText('dashboard.time.inMinutes:17')).toBeInTheDocument()
  })
  it('flags invalid schedules', () => {
    render(<WorkflowsCard workflows={wfs} executions={[]} {...noop}
      schedules={{ daemon_running: true, upcoming: [], invalid: [{ workflow_id: 'w2', node_id: 'n', error: 'bad' }] }} />)
    expect(screen.getByTitle('bad')).toBeInTheDocument()
  })
  it('run button calls onRun; a live run shows Stop', () => {
    const onRun = vi.fn(() => Promise.resolve())
    const { rerender } = render(<WorkflowsCard workflows={[wfs[1]]} executions={[]} schedules={null} {...noop} onRun={onRun} />)
    fireEvent.click(screen.getByText('dashboard.workflows.run'))
    expect(onRun).toHaveBeenCalledWith('w2')
    const onStop = vi.fn(() => Promise.resolve())
    rerender(<WorkflowsCard workflows={[wfs[1]]} executions={[{ id: 'e9', workflow_id: 'w2', status: 'RUNNING' }]} schedules={null} {...noop} onStop={onStop} />)
    fireEvent.click(screen.getByText('dashboard.workflows.stop'))
    expect(onStop).toHaveBeenCalledWith('e9')
  })
  it('empty state', () => {
    render(<WorkflowsCard workflows={[]} executions={[]} schedules={null} {...noop} />)
    expect(screen.getByText('dashboard.workflowsSection.emptyTitle')).toBeInTheDocument()
  })
})

describe('RecentRunsCard', () => {
  it('lists runs and opens one in the editor', () => {
    const onNavigate = vi.fn()
    render(<RecentRunsCard executions={[{ id: 'e1', workflow_id: 'w1', workflow_name: 'Scraper', status: 'FAILED', error: 'boom' }]} onNavigate={onNavigate} />)
    expect(screen.getByText('boom')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Scraper'))
    expect(onNavigate).toHaveBeenCalledWith('noderunner', { executionId: 'e1', workflowId: 'w1' })
  })
  it('empty', () => {
    render(<RecentRunsCard executions={[]} onNavigate={vi.fn()} />)
    expect(screen.getByText('dashboard.recentRuns.empty')).toBeInTheDocument()
  })
})

describe('WorkflowsCard in the All profiles view', () => {
  const mixed = [
    { id: 'w1', name: 'Scraper', is_active: true, profile_id: 'default', profile_name: 'Default' },
    { id: 'w9', name: 'Outreach', is_active: true, profile_id: 'p-work', profile_name: 'Work' },
  ]
  it('another profile\'s row shows its profile and Switch instead of Run', () => {
    const onSwitch = vi.fn()
    render(<WorkflowsCard workflows={mixed} executions={[]} schedules={null} currentId="default" onSwitch={onSwitch} {...noop} />)
    expect(screen.getByText('Work')).toBeInTheDocument()
    expect(screen.getAllByText('dashboard.workflows.run')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'dashboard.profiles.switch' }))
    expect(onSwitch).toHaveBeenCalledWith('p-work')
  })
  it('recent runs from another profile switch instead of opening', () => {
    const onSwitch = vi.fn()
    const onNavigate = vi.fn()
    render(<RecentRunsCard currentId="default" onSwitch={onSwitch} onNavigate={onNavigate}
      executions={[{ id: 'e9', workflow_id: 'w9', workflow_name: 'Outreach', status: 'FAILED', profile_id: 'p-work', profile_name: 'Work', created_at: new Date().toISOString() }]} />)
    fireEvent.click(screen.getByText('Outreach'))
    expect(onSwitch).toHaveBeenCalledWith('p-work')
    expect(onNavigate).not.toHaveBeenCalled()
  })
})
