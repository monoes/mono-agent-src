// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'
import StopPanel from './StopPanel.jsx'

afterEach(cleanup)

// The control room's Stop reads a result the way the chat header's Stop does: exit 8 with a "stopped" first line is a warning
// (the run is stopped, its cleanup is not), a repairing stop is a success, and only the rest is "NOT confirmed stopped".
async function stopWith(res) {
  const api = { stop: vi.fn().mockResolvedValue(res) }
  render(<StopPanel api={api} />)
  fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
  fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
  await waitFor(() => expect(screen.getByTestId('cr-stop-result')).toBeInTheDocument())
  return screen.getByTestId('cr-stop-result')
}

describe('StopPanel result', () => {
  it('stopped with a failed cleanup (exit 8) is a warning with a retry, not NOT confirmed stopped', async () => {
    const r = await stopWith({ ok: false, stopped: false, output: 'stopped\nsession 4 NOT ended: boom', detail: 'exit status 8', code: '' })
    expect(r).toHaveAttribute('data-ok', 'warn')
    expect(r.textContent).toContain('Stopped, but the cleanup did not finish')
    expect(r.textContent).not.toContain('NOT confirmed stopped')
    expect(screen.getByRole('button', { name: 'Retry cleanup' })).toBeEnabled()
  })

  it('a successful repair is a success', async () => {
    const r = await stopWith({ ok: true, stopped: false, output: 'no open run; ended sessions left open by the previous stop: 4', detail: '', code: '' })
    expect(r).toHaveAttribute('data-ok', 'yes')
    expect(r.textContent).not.toContain('NOT confirmed stopped')
  })

  it('a plain stop is a success and NOT stopped is the failure', async () => {
    expect(await stopWith({ ok: true, stopped: true, output: 'stopped', detail: '', code: '' })).toHaveAttribute('data-ok', 'yes')
    cleanup()
    const r = await stopWith({ ok: false, stopped: false, output: 'NOT stopped: 1 process(es) survive', detail: 'exit status 1', code: '' })
    expect(r).toHaveAttribute('data-ok', 'no')
    expect(r.textContent).toContain('NOT confirmed stopped')
  })
})
