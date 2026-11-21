// @vitest-environment jsdom
// System check: four rows read from the doctor report, the fixes the report
// offers (with their streamed progress), and the system mark once all pass.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act, fireEvent, within } from '@testing-library/react'
import i18n from '../../i18n.js'

const go = vi.hoisted(() => ({ RunHealthCheck: vi.fn(), RunHealthFix: vi.fn() }))
const events = vi.hoisted(() => ({ listeners: [] }))
vi.mock('../../wailsjs/go/main/App', () => go)
vi.mock('../../services/api.js', () => ({
  subscribeEvent: vi.fn((name, cb) => {
    const entry = { name, cb }
    events.listeners.push(entry)
    return () => { events.listeners = events.listeners.filter(e => e !== entry) }
  }),
}))

import SystemCheck, { HEALTH_MODE, RETRY_MS } from './SystemCheck.jsx'

const fix = (id, safety = 'confirm') => ({ id, label: `label of ${id}`, safety })
const row = (id, status, extra = {}) => ({ id, group: id.split('.')[0], title: id, status, summary: `${id} ${status}`, ...extra })
const report = (rows) => JSON.stringify({ v: 1, profile_id: '', results: [...rows, row('core.db', 'ok')] })
const FAILING = report([
  row('core.cli', 'info', { summary: 'v1.2.3 (/usr/local/bin/monoagentcli)' }),
  row('monomind.node', 'ok'),
  row('monomind.binary', 'fail', { summary: 'not installed', fix: fix('monomind.install') }),
  row('services.daemon', 'warn', { summary: 'not running', fix: fix('services.daemon.start') }),
])
const PASSING = report([
  row('core.cli', 'info'),
  row('monomind.node', 'ok'),
  row('monomind.binary', 'ok'),
  row('services.daemon', 'ok'),
])
const emit = (ev) => act(async () => { for (const l of [...events.listeners]) if (l.name === 'health:fixProgress') l.cb(ev) })
const flush = () => act(async () => { await new Promise(r => setTimeout(r, 0)) })
const rowOf = (title) => screen.getAllByRole('listitem').find(li => li.querySelector('b')?.textContent === title)

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  events.listeners = []
  go.RunHealthCheck.mockReset()
  go.RunHealthFix.mockReset()
  props = {
    mark: vi.fn(() => Promise.resolve({ ok: true })),
    setCanContinue: vi.fn(),
    setOnContinue: vi.fn(),
    setContinueLabel: vi.fn(),
  }
})
afterEach(cleanup)

describe('SystemCheck', () => {
  it('shows a Fix button only on failing rows and enables Continue when all four pass', async () => {
    go.RunHealthCheck.mockResolvedValueOnce(FAILING).mockResolvedValueOnce(PASSING)
    go.RunHealthFix.mockResolvedValue('{"ok":true}')
    const { container } = render(<SystemCheck {...props} />)
    // Before the one report arrives every row is checking; nothing is green.
    expect(screen.getAllByRole('listitem')).toHaveLength(4)
    expect(screen.getAllByText('Checking')).toHaveLength(4)
    await flush()

    expect(HEALTH_MODE).toBe('background')
    expect(go.RunHealthCheck).toHaveBeenCalledWith('background')
    expect(within(rowOf('MonoAgent command line')).queryByRole('button')).toBeNull()
    expect(within(rowOf('MonoAgent command line')).getByText('Found')).toBeInTheDocument()
    expect(within(rowOf('Node.js')).queryByRole('button')).toBeNull()
    expect(within(rowOf('monomind')).getByRole('button', { name: 'Install' })).toBeInTheDocument()
    expect(within(rowOf('monomind')).getByText('Not installed')).toBeInTheDocument()
    expect(within(rowOf('Background service')).getByText(/lets the browser extension connect/)).toBeInTheDocument()
    expect(props.setCanContinue).not.toHaveBeenCalledWith(true)
    expect(props.mark).not.toHaveBeenCalled()

    fireEvent.click(within(rowOf('Background service')).getByRole('button', { name: 'Start' }))
    await flush()
    expect(go.RunHealthFix).toHaveBeenCalledWith('services.daemon.start')
    await emit({ fix_id: 'services.daemon.start', kind: 'line', message: 'starting the daemon' })
    expect(within(rowOf('Background service')).getByText('starting the daemon')).toBeInTheDocument()
    expect(within(rowOf('Background service')).getByText('Starting')).toBeInTheDocument()
    await emit({ fix_id: 'other.fix', kind: 'done' }) // someone else's fix changes nothing
    expect(go.RunHealthCheck).toHaveBeenCalledTimes(1)

    await emit({ fix_id: 'services.daemon.start', kind: 'done', message: '' })
    await flush()
    // Green only from the new report, never from the done event.
    expect(go.RunHealthCheck).toHaveBeenCalledTimes(2)
    expect(screen.queryAllByRole('button')).toHaveLength(0)
    expect(within(rowOf('Background service')).getByText('Running')).toBeInTheDocument()
    expect(container.querySelectorAll('.onb-row.ok')).toHaveLength(4)
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
    expect(props.mark).toHaveBeenCalledTimes(1)
    expect(props.mark).toHaveBeenCalledWith('system')
    expect(events.listeners).toHaveLength(0)
  })

  it('a failed fix shows the error text and leaves the row failing', async () => {
    go.RunHealthCheck.mockResolvedValue(FAILING)
    go.RunHealthFix.mockResolvedValue('{"ok":true}')
    render(<SystemCheck {...props} />)
    await flush()
    fireEvent.click(within(rowOf('monomind')).getByRole('button', { name: 'Install' }))
    await flush()
    expect(go.RunHealthFix).toHaveBeenCalledWith('monomind.install')
    expect(within(rowOf('monomind')).getByText('Installing')).toBeInTheDocument()
    await emit({ fix_id: 'monomind.install', kind: 'error', message: 'npm ERR! EACCES: permission denied' })
    await flush()
    const r = rowOf('monomind')
    expect(within(r).getByText('npm ERR! EACCES: permission denied')).toBeInTheDocument()
    expect(within(r).getByText('Not installed')).toBeInTheDocument()
    expect(r).toHaveClass('bad')
    expect(within(r).getByRole('button', { name: 'Install' })).toBeEnabled()
    expect(go.RunHealthCheck).toHaveBeenCalledTimes(1)
    expect(props.setCanContinue).not.toHaveBeenCalledWith(true)
  })

  it('a fix refused at once shows its error and stops the spinner', async () => {
    go.RunHealthCheck.mockResolvedValue(FAILING)
    go.RunHealthFix.mockResolvedValue('{"error":"monoagentcli not found in PATH"}')
    const { container } = render(<SystemCheck {...props} />)
    await flush()
    fireEvent.click(within(rowOf('monomind')).getByRole('button', { name: 'Install' }))
    await flush()
    expect(within(rowOf('monomind')).getByText('monoagentcli not found in PATH')).toBeInTheDocument()
    expect(container.querySelector('.onb-spinner')).toBeNull()
    expect(events.listeners).toHaveLength(0)
  })

  it('a fix already running (started before) is followed to its end, not shown as a failure', async () => {
    go.RunHealthCheck.mockResolvedValueOnce(FAILING).mockResolvedValueOnce(PASSING)
    go.RunHealthFix.mockResolvedValue('{"error":"monomind.install is already running"}')
    render(<SystemCheck {...props} />)
    await flush()
    fireEvent.click(within(rowOf('monomind')).getByRole('button', { name: 'Install' }))
    await flush()
    expect(screen.queryByText(/already running/)).toBeNull()
    expect(within(rowOf('monomind')).getByText('Installing')).toBeInTheDocument()
    await emit({ fix_id: 'monomind.install', kind: 'line', message: 'added 212 packages' })
    expect(within(rowOf('monomind')).getByText('added 212 packages')).toBeInTheDocument()
    await emit({ fix_id: 'monomind.install', kind: 'done' })
    await flush()
    expect(go.RunHealthCheck).toHaveBeenCalledTimes(2)
    expect(within(rowOf('monomind')).getByText('Installed')).toBeInTheDocument()
  })

  it('a check already running (left and came back) is asked again, not shown as a failure', async () => {
    vi.useFakeTimers()
    try {
      go.RunHealthCheck.mockResolvedValueOnce('{"error":"a background check is already running"}')
        .mockResolvedValueOnce('{"error":"a background check is already running"}')
        .mockResolvedValueOnce(PASSING)
      render(<SystemCheck {...props} />)
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })
      expect(go.RunHealthCheck).toHaveBeenCalledTimes(1)
      expect(screen.queryByRole('alert')).toBeNull()
      expect(screen.getAllByText('Checking')).toHaveLength(4)
      await act(async () => { await vi.advanceTimersByTimeAsync(RETRY_MS) })
      expect(go.RunHealthCheck).toHaveBeenCalledTimes(2)
      expect(screen.queryByRole('alert')).toBeNull()
      await act(async () => { await vi.advanceTimersByTimeAsync(RETRY_MS) })
      expect(go.RunHealthCheck).toHaveBeenCalledTimes(3)
      expect(within(rowOf('Background service')).getByText('Running')).toBeInTheDocument()
      expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a login-required answer shows the sign-in message and stops the spinner', async () => {
    go.RunHealthCheck.mockResolvedValueOnce('{"error":"Log in to monoes.me first: run monoagentcli login","login_required":true}')
      .mockResolvedValueOnce(PASSING)
    const { container } = render(<SystemCheck {...props} />)
    expect(container.querySelector('.onb-spinner')).not.toBeNull()
    await flush()
    expect(screen.getByRole('alert')).toHaveTextContent('Your sign-in has ended. Sign in to monoes.me to continue.')
    expect(container.querySelector('.onb-spinner')).toBeNull()
    expect(screen.getAllByText('Not checked')).toHaveLength(4)
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await flush()
    expect(go.RunHealthCheck).toHaveBeenCalledTimes(2)
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
  })

  it('a missing command line fails its row and leaves the rest unchecked', async () => {
    go.RunHealthCheck.mockResolvedValue('{"error":"monoagentcli not found","cli_missing":true}')
    render(<SystemCheck {...props} />)
    await flush()
    expect(within(rowOf('MonoAgent command line')).getByText('Not found')).toBeInTheDocument()
    expect(rowOf('MonoAgent command line')).toHaveClass('bad')
    expect(screen.getAllByText('Not checked')).toHaveLength(3)
    expect(screen.getByRole('alert')).toHaveTextContent(/could not find its command line/)
  })

  it('a manual fix shows its command instead of a button', async () => {
    go.RunHealthCheck.mockResolvedValue(report([
      row('core.cli', 'info'),
      row('monomind.node', 'ok'),
      row('monomind.binary', 'warn', { summary: 'an older monomind comes first', fix: { ...fix('monomind.shadowed', 'manual'), command: 'remove the older monomind' } }),
      row('services.daemon', 'ok'),
    ]))
    render(<SystemCheck {...props} />)
    await flush()
    const r = rowOf('monomind')
    expect(within(r).queryByRole('button')).toBeNull()
    expect(within(r).getByText('remove the older monomind')).toBeInTheDocument()
  })
})
