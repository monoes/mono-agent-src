// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, renderHook } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({ api: { openPathWithOS: vi.fn() }, notify: vi.fn() }))

import { CoderHeader } from './CoderHeader.jsx'
import { showStop, stopLine, transcriptMessage } from './OpenEndedStop.jsx'
import { useRunExists } from './useRunExists.js'

afterEach(cleanup)

const chatMode = mode => ({ state: { mode, locked: true, options: [] }, busy: false, error: '', set: vi.fn() })
const header = (mode, runExists, extra = {}) => {
  const api = { stop: vi.fn() }
  const onStopped = vi.fn()
  render(<CoderHeader cwd="/w" chatMode={chatMode(mode)} openEnded={{ runExists, conversationId: 'c1', onStopped, api, ...extra }} />)
  return { api, onStopped }
}

describe('who sees the Stop button', () => {
  it('only an open-ended chat that may have a run, or any chat of a profile with a run', () => {
    expect([
      showStop('off', null), showStop('mission', false), showStop('off', false), showStop('mission', null),
      showStop('open', null), showStop('open', true), showStop('off', true), showStop('open', false),
    ]).toEqual([false, false, false, false, true, true, true, false])
  })

  it('a normal chat (off or mission) with no run shows no Stop button', () => {
    for (const m of ['off', 'mission']) {
      for (const r of [null, false]) {
        header(m, r)
        expect(screen.queryByTestId('chat-stop')).toBeNull()
        cleanup()
      }
    }
  })

  it('every chat of a profile with a run shows it, whatever its mode', () => {
    for (const m of ['off', 'mission', 'open']) {
      header(m, true)
      expect(screen.getByRole('button', { name: /Stop run/ })).toBeInTheDocument()
      cleanup()
    }
  })

  it('an open-ended chat shows it while the run state is unknown (a signed-out window cannot read it)', () => {
    header('open', null)
    expect(screen.getByRole('button', { name: /Stop run/ })).toBeInTheDocument()
  })
})

describe('the two-step stop', () => {
  it('calls nothing until the confirm, then hands the CLI\'s own message to the transcript', async () => {
    const { api, onStopped } = header('open', true)
    api.stop.mockResolvedValue({ ok: true, stopped: true, output: 'stopped\nsessions ended: 4\ntranscript c1: Open-ended run stopped by you at T: stopped\n', detail: '', code: '' })
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    expect(api.stop).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(onStopped).toHaveBeenCalledWith('Open-ended run stopped by you at T: stopped'))
    expect(api.stop).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'yes')
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('sessions ended: 4')
  })

  it('cancel calls nothing', () => {
    const { api } = header('open', true)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(api.stop).not.toHaveBeenCalled()
    expect(screen.queryByRole('alertdialog')).toBeNull()
  })

  it('a stop that failed says so, shows the CLI\'s words and leaves no message in the transcript', async () => {
    const { api, onStopped } = header('open', true)
    api.stop.mockResolvedValue({ ok: false, stopped: false, output: 'NOT stopped: 1 process(es) survive: pid 7 (x)', detail: 'exit status 1', code: '' })
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'no'))
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('NOT stopped: 1 process(es) survive')
    expect(onStopped).not.toHaveBeenCalled()
  })

  it('a best-effort stop shows its words verbatim, never a bare "stopped"', async () => {
    const { api, onStopped } = header('open', true)
    api.stop.mockResolvedValue({ ok: true, stopped: true, output: 'stopped (best effort: processes were not proven gone)', detail: '', code: '' })
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(onStopped).toHaveBeenCalled())
    expect(onStopped.mock.calls[0][0]).toContain('stopped (best effort: processes were not proven gone)')
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('stopped (best effort: processes were not proven gone)')
  })
})

describe('what the result says about the parts that did not work', () => {
  const stopWith = async (res) => {
    const { api, onStopped } = header('open', true)
    api.stop.mockResolvedValue(res)
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(screen.getByTestId('chat-stop-result')).toBeInTheDocument())
    return onStopped
  }

  it('shows "sessions NOT ended" and "transcript NOT written" as warnings, and the good lines as plain', async () => {
    await stopWith({ ok: true, stopped: true, output: 'stopped\nsession 4 NOT ended: db locked\ntranscript NOT written (c1): no chat store is wired\nsessions ended: 5', detail: '', code: '' })
    const warns = screen.getAllByTestId('chat-stop-warn').map(n => n.textContent)
    expect(warns).toEqual(['session 4 NOT ended: db locked', 'transcript NOT written (c1): no chat store is wired'])
  })

  it('a message the CLI did not save is shown as local, and says so', async () => {
    const onStopped = await stopWith({ ok: true, stopped: true, output: 'stopped', detail: '', code: '' })
    expect(onStopped.mock.calls[0][0]).toMatch(/shown here only/)
  })

  it('a later stop that finished the previous stop is a success, not "NOT confirmed stopped"', async () => {
    const onStopped = await stopWith({ ok: true, stopped: false, output: 'no open run; ended sessions left open by the previous stop: 4\ntranscript c1: Open-ended run stopped by you at T: stopped (x)', detail: '', code: '' })
    expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'yes')
    expect(onStopped).toHaveBeenCalledWith('Open-ended run stopped by you at T: stopped (x)')
  })

  it('a failed repair is a failure', async () => {
    await stopWith({ ok: false, stopped: false, output: 'no open run; ended sessions left open by the previous stop: 4\ntranscript NOT written (c1): x', detail: 'exit status 1', code: '' })
    expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'no')
  })
})

describe('reading the CLI output', () => {
  it('finds the verdict line and only this chat\'s transcript line', () => {
    const out = 'stopped\nsessions ended: 1, 2\ntranscript c1: first\ntranscript c2: second\n'
    expect(stopLine(out)).toBe('stopped')
    expect(stopLine('NOT stopped:\n  x')).toBe('NOT stopped:')
    expect(transcriptMessage(out, 'c2')).toBe('second')
    expect(transcriptMessage(out, 'c3')).toBe('')
  })
})

describe('useRunExists', () => {
  it('is null until a definite answer, and a failed probe (signed out) keeps it null', async () => {
    const api = { probe: vi.fn().mockResolvedValue({ error: 'login required' }) }
    const { result } = renderHook(() => useRunExists(true, api))
    await waitFor(() => expect(api.probe).toHaveBeenCalled())
    expect(result.current).toBeNull()
  })
  it('follows run_exists, and asks nothing when disabled', async () => {
    const api = { probe: vi.fn().mockResolvedValue({ mode_available: true, run_exists: true }) }
    const { result } = renderHook(() => useRunExists(true, api))
    await waitFor(() => expect(result.current).toBe(true))
    const off = { probe: vi.fn() }
    renderHook(() => useRunExists(false, off))
    expect(off.probe).not.toHaveBeenCalled()
  })
})
