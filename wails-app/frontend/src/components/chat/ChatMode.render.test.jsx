// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import '../../i18n.js'

// The chat mode selector in the coder header: three modes chosen before the first message, locked after it, with the
// CLI's reasons for a disabled mode. The CLI decides; this shows it.
const { api } = vi.hoisted(() => ({ api: { getChatMissionToggle: vi.fn(), setChatMode: vi.fn(), getChatModeOptions: vi.fn(), openPathWithOS: vi.fn() } }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn() }))
const { openStartApi } = vi.hoisted(() => ({ openStartApi: { entry: vi.fn() } }))
vi.mock('../../lib/openStartApi.js', () => ({ openStartApi }))

import { CoderHeader } from './CoderHeader.jsx'
import { ChatModeChip } from './ChatModeSelector.jsx'
import { useChatMode } from './useChatMode.js'

const OPTIONS = [
  { mode: 'off', available: true },
  { mode: 'mission', available: true },
  { mode: 'open', available: false, reason: 'not available yet' },
]
const mode = (over = {}) => ({ mode: 'off', locked: false, options: OPTIONS, ...over })
const view = m => ({ conversationId: 'c1', chatMode: m })

function Harness({ id = 'c1', enabled = true, access = 'full', onMode }) {
  const chatMode = useChatMode(id, enabled, access)
  if (onMode) onMode(chatMode)
  return <><ChatModeChip state={chatMode.state} /><CoderHeader cwd="/work/app" chatMode={chatMode} /></>
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getChatMissionToggle.mockResolvedValue(view(mode()))
  api.getChatModeOptions.mockResolvedValue({ access: 'full', options: OPTIONS })
})
afterEach(cleanup)

const radio = name => screen.findByRole('radio', { name })

describe('chat mode selector', () => {
  it('shows the three modes with the current one checked, and the chip with the same value', async () => {
    render(<Harness />)
    expect(await radio('Off')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'Mission' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByRole('radio', { name: 'Open-ended' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByTestId('chat-mode-chip')).toHaveAttribute('data-mode', 'off')
  })

  it('disables open-ended with the CLI\'s reason as its tooltip, and does not call the CLI for it', async () => {
    render(<Harness />)
    const open = await radio('Open-ended')
    expect(open).toBeDisabled()
    expect(open.parentElement).toHaveAttribute('title', 'not available yet')
    fireEvent.click(open)
    expect(api.setChatMode).not.toHaveBeenCalled()
  })

  it('chooses a mode through the CLI and shows what it stored, in the chip too', async () => {
    api.setChatMode.mockResolvedValue(view(mode({ mode: 'mission' })))
    render(<Harness />)
    fireEvent.click(await radio('Mission'))
    await waitFor(() => expect(screen.getByRole('radio', { name: 'Mission' })).toHaveAttribute('aria-checked', 'true'))
    expect(api.setChatMode).toHaveBeenCalledWith('c1', 'mission')
    expect(screen.getByTestId('chat-mode-chip')).toHaveAttribute('data-mode', 'mission')
  })

  it('is disabled once the CLI says the chat has its first message, with the reason as a tooltip and shown', async () => {
    const reason = 'the chat mode is chosen before the first message and cannot change afterwards'
    api.getChatMissionToggle.mockResolvedValue(view(mode({ mode: 'mission', locked: true, reason })))
    render(<Harness />)
    const m = await radio('Mission')
    await waitFor(() => expect(m).toBeDisabled())
    expect(m).toHaveAttribute('aria-checked', 'true')
    for (const name of ['Off', 'Mission', 'Open-ended']) expect(screen.getByRole('radio', { name })).toBeDisabled()
    expect(screen.getByTestId('chat-mode')).toHaveAttribute('title', reason)
    expect(screen.getByTestId('chat-mode-reason')).toHaveTextContent('cannot change afterwards')
    fireEvent.click(screen.getByRole('radio', { name: 'Off' }))
    expect(api.setChatMode).not.toHaveBeenCalled()
  })

  it('disables mission below coding access with the CLI\'s reason', async () => {
    const reason = 'a mission needs coding, workspace or full access; this chat has scoped access'
    api.getChatMissionToggle.mockResolvedValue(view(mode({ options: [OPTIONS[0], { mode: 'mission', available: false, reason }, OPTIONS[2]] })))
    render(<Harness />)
    const m = await radio('Mission')
    expect(m).toBeDisabled()
    expect(m.parentElement).toHaveAttribute('title', reason)
  })

  it("shows the CLI's refusal inline and keeps the stored state", async () => {
    api.setChatMode.mockRejectedValue(new Error('the chat mode is chosen before the first message'))
    render(<Harness />)
    fireEvent.click(await radio('Mission'))
    expect(await screen.findByTestId('chat-mode-error')).toHaveTextContent('Could not change the chat mode: the chat mode is chosen before the first message')
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Off' })).toHaveAttribute('aria-checked', 'true')
  })

  it('shows a load failure inline', async () => {
    api.getChatMissionToggle.mockRejectedValue(new Error('boom'))
    render(<Harness />)
    expect(await screen.findByTestId('chat-mode-error')).toHaveTextContent('Could not read the chat mode')
  })

  it('shows nothing for a CLI that does not know the mode', async () => {
    api.getChatMissionToggle.mockResolvedValue({ conversationId: 'c1' })
    render(<Harness />)
    await waitFor(() => expect(api.getChatMissionToggle).toHaveBeenCalled())
    expect(screen.queryByRole('radiogroup')).toBeNull()
    expect(screen.queryByTestId('chat-mode-chip')).toBeNull()
  })

  it('before the chat exists reads the options of the access level, writes nothing, and hands the choice to the creation', async () => {
    let hook
    render(<Harness id="" onMode={x => { hook = x }} />)
    fireEvent.click(await radio('Mission'))
    await waitFor(() => expect(screen.getByRole('radio', { name: 'Mission' })).toHaveAttribute('aria-checked', 'true'))
    expect(api.getChatModeOptions).toHaveBeenCalledWith('full')
    expect(api.getChatMissionToggle).not.toHaveBeenCalled()
    expect(api.setChatMode).not.toHaveBeenCalled()
    expect(hook.pendingMode()).toBe('mission')
    expect(screen.getByRole('radio', { name: 'Open-ended' })).toBeDisabled()
  })

  it('offers nothing to the creation when the selector is not enabled (below coder access)', async () => {
    let hook
    render(<Harness id="" enabled={false} access="scoped" onMode={x => { hook = x }} />)
    await waitFor(() => expect(hook).toBeTruthy())
    expect(hook.pendingMode()).toBe('')
    expect(screen.queryByRole('radiogroup')).toBeNull()
  })

  it('is reachable by keyboard: real buttons in tab order', async () => {
    render(<Harness />)
    const m = await radio('Mission')
    expect(m.tagName).toBe('BUTTON')
    m.focus()
    expect(m).toHaveFocus()
  })
})

describe('the open mode is probed only on the person\'s action', () => {
  const PENDING = [{ mode: 'off', available: true }, { mode: 'mission', available: true }, { mode: 'open', available: false, pending: true, reason: 'not checked yet' }]
  it('does not probe on mount or on a timer, only when the pending open-ended option is pressed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      api.getChatMissionToggle.mockResolvedValue(view(mode({ options: PENDING })))
      api.setChatMode.mockResolvedValue(view(mode({ mode: 'open', options: PENDING })))
      openStartApi.entry.mockResolvedValue({ mode_available: true })
      render(<Harness />)
      const open = await radio('Open-ended')
      await act(async () => { vi.advanceTimersByTime(120000) }) // several polls of the settings: none of them probes
      expect(openStartApi.entry).not.toHaveBeenCalled()
      fireEvent.click(open)
      await waitFor(() => expect(openStartApi.entry).toHaveBeenCalledTimes(1))
      await waitFor(() => expect(api.setChatMode).toHaveBeenCalledWith('c1', 'open'))
    } finally { vi.useRealTimers() }
  })
  it('does not switch to open when the probe says it cannot start, and shows why', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mode({ options: PENDING })))
    openStartApi.entry.mockResolvedValue({ mode_available: false, reason: 'containment unavailable: pidfd_open' })
    render(<Harness />)
    fireEvent.click(await radio('Open-ended'))
    expect(await screen.findByTestId('chat-mode-error')).toHaveTextContent('pidfd_open')
    expect(api.setChatMode).not.toHaveBeenCalled()
  })
})

// A disabled mode says WHY on the radio itself (title and description for assistive technology) and in a visible line, from the
// CLI's own reason; with no reason it still says it is not available.
describe('why a mode is not available', () => {
  const unavailable = reason => mode({ options: [OPTIONS[0], OPTIONS[1], { mode: 'open', available: false, ...(reason ? { reason } : {}) }] })
  it('shows the reason on the disabled radio and under the selector', async () => {
    const why = 'an open-ended run is already active in this profile: only one runs at a time, stop it first'
    api.getChatMissionToggle.mockResolvedValue(view(unavailable(why)))
    render(<Harness />)
    const open = await radio('Open-ended')
    expect(open).toBeDisabled()
    expect(open).toHaveAttribute('title', why)
    expect(open).toHaveAttribute('aria-description', why)
    expect(screen.getByTestId('chat-mode-why-open')).toHaveTextContent('Open-ended is not available: ' + why)
  })
  it('shows the reason of the binding service on the disabled open radio on Windows when the binding cannot run', async () => {
    const why = 'not available on this machine: the run needs the peer-credential binding, and this Windows cannot tell which process connected to a local socket'
    api.getChatMissionToggle.mockResolvedValue(view(unavailable(why)))
    render(<Harness />)
    const open = await radio('Open-ended')
    expect(open).toBeDisabled()
    expect(open).toHaveAttribute('title', why)
    expect(screen.getByTestId('chat-mode-why-open')).toHaveTextContent(why)
  })
  it('says "Not available" when the CLI gave no reason', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(unavailable('')))
    render(<Harness />)
    const open = await radio('Open-ended')
    expect(open).toHaveAttribute('title', 'Not available')
    expect(screen.getByTestId('chat-mode-why-open')).toHaveTextContent('Open-ended is not available: Not available')
  })
  it('shows no such line for an available mode, nor once the chat is locked (the locked reason is shown instead)', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mode({ options: [OPTIONS[0], OPTIONS[1], { mode: 'open', available: true }] })))
    const { unmount } = render(<Harness />)
    await radio('Open-ended')
    expect(screen.queryByTestId('chat-mode-why-open')).toBeNull()
    unmount()
    api.getChatMissionToggle.mockResolvedValue(view(mode({ locked: true, reason: 'locked: it has a message', options: [OPTIONS[0], OPTIONS[1], { mode: 'open', available: false, reason: 'x' }] })))
    render(<Harness />)
    await radio('Open-ended')
    expect(screen.queryByTestId('chat-mode-why-open')).toBeNull()
    expect(screen.getByTestId('chat-mode-reason')).toHaveTextContent('locked: it has a message')
  })
})
