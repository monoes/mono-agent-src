// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, renderHook } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({ api: { openPathWithOS: vi.fn() }, notify: vi.fn() }))

import { CoderHeader } from './CoderHeader.jsx'
import { showStop, stopLine, transcriptMessage } from './OpenEndedStop.jsx'
import { outcome } from './stopOutcome.js'
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

  it('a stop that finds no open run is "already stopped": no failure, no Stop button, the header refreshes', async () => {
    const { api, onStopped } = header('open', null)
    api.stop.mockResolvedValue({ ok: false, stopped: false, output: '', detail: 'no open run to stop', code: '' })
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'yes'))
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('already stopped')
    expect(screen.queryByText('NOT confirmed stopped')).toBeNull()
    expect(screen.queryByRole('button', { name: /Stop run/ })).toBeNull()
    expect(onStopped).toHaveBeenCalledWith('')
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

  it('a failed repair is a warning, not a success and not "NOT confirmed stopped"', async () => {
    await stopWith({ ok: false, stopped: false, output: 'no open run; ended sessions left open by the previous stop: 4\ntranscript NOT written (c1): x', detail: 'exit status 8', code: '' })
    expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'warn')
  })

  it('a stop whose cleanup failed (exit 8, first line stopped) is a warning with a retry, and still leaves its message', async () => {
    const onStopped = await stopWith({ ok: false, stopped: false, output: 'stopped\nsession 4 NOT ended: boom', detail: 'exit status 8', code: '' })
    expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'warn')
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('Stopped, but the cleanup did not finish')
    expect(screen.getByRole('button', { name: 'Retry cleanup' })).toBeEnabled()
    expect(onStopped).toHaveBeenCalled()
  })

  it('a NOT stopped result is still the failure', async () => {
    await stopWith({ ok: false, stopped: false, output: 'NOT stopped: 1 process(es) survive: pid 7', detail: 'exit status 1', code: '' })
    expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'no')
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('NOT confirmed stopped')
  })
})

describe('the result outlives the probe, and the dialog is accessible', () => {
  const mount = (runExists) => {
    const api = { stop: vi.fn().mockResolvedValue({ ok: true, stopped: true, output: 'stopped\nsessions ended: 4', detail: '', code: '' }) }
    const props = { conversationId: 'c1', onStopped: vi.fn(), api }
    const ui = r => <CoderHeader cwd="/w" chatMode={chatMode('open')} openEnded={{ ...props, runExists: r }} />
    const view = render(ui(runExists))
    return { api, view, ui }
  }
  const doStop = async () => {
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(screen.getByTestId('chat-stop-result')).toBeInTheDocument())
  }

  it('keeps the result when the probe says there is no run any more, and offers no second Stop', async () => {
    const { view, ui } = mount(true)
    await doStop()
    expect(screen.getByRole('button', { name: 'Stopped, refreshing...' })).toBeDisabled() // the probe has not caught up
    view.rerender(ui(false))
    expect(screen.getByTestId('chat-stop-result').textContent).toContain('sessions ended: 4')
    expect(screen.queryByRole('button', { name: /Stop run|Retry cleanup/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByTestId('chat-stop')).toBeNull()
  })

  it('the confirm dialog is modal, takes focus, closes on Escape and gives focus back', () => {
    mount(true)
    const open = screen.getByRole('button', { name: /Stop run/ })
    fireEvent.click(open)
    const dialog = screen.getByRole('alertdialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: /Stop run/ }))
  })
})

describe('retry survives the probe', () => {
  it('keeps the Retry cleanup button after a partial failure even when the probe says there is no run', async () => {
    const api = { stop: vi.fn().mockResolvedValue({ ok: false, stopped: false, output: 'stopped\nsession 4 NOT ended: boom', detail: 'exit status 8', code: '' }) }
    const props = { conversationId: 'c1', onStopped: vi.fn(), api }
    const ui = r => <CoderHeader cwd="/w" chatMode={chatMode('open')} openEnded={{ ...props, runExists: r }} />
    const view = render(ui(true))
    fireEvent.click(screen.getByRole('button', { name: /Stop run/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'warn'))
    view.rerender(ui(false)) // a stopped run is quiescent: the probe flips right away
    expect(screen.getByRole('button', { name: 'Retry cleanup' })).toBeEnabled()
    api.stop.mockResolvedValue({ ok: true, stopped: false, output: 'no open run; ended sessions left open by the previous stop: 4', detail: '', code: '' })
    fireEvent.click(screen.getByRole('button', { name: 'Retry cleanup' }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop the run' }))
    await waitFor(() => expect(screen.getByTestId('chat-stop-result')).toHaveAttribute('data-ok', 'yes'))
    expect(screen.queryByRole('button', { name: /Stop run|Retry cleanup/ })).toBeNull()
  })
})

describe('the repair heads the CLI prints (one fixture, read by Go too)', () => {
  it('a repair that ended sessions or wrote the owed message is a success; a failed one is a warning; other text is neither', async () => {
    const heads = (await import('./stopOutput.fixture.json')).default
    const ok = { ok: true, stopped: false, detail: '', code: '' }
    expect(outcome({ ...ok, output: heads.repairEnded + ': 4, 5' })).toBe('ok')
    expect(outcome({ ...ok, output: heads.repairNote + '\ntranscript c1: m' })).toBe('ok')
    expect(outcome({ ok: false, stopped: false, output: heads.repairFailed })).toBe('warn')
    expect(outcome({ ok: false, stopped: false, output: heads.repairEnded + ': 4\ntranscript NOT written (c1): x' })).toBe('warn')
    expect(outcome({ ...ok, output: 'no open run; something else' })).toBe('fail')
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
