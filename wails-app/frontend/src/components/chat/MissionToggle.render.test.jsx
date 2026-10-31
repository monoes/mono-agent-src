// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

// The Mission switch in the coder header: off, on, locked by an open mission session, disabled
// below coding access, and the CLI's refusal shown inline. The CLI decides; this shows it.
const { api } = vi.hoisted(() => ({ api: { getChatMissionToggle: vi.fn(), setChatMissionToggle: vi.fn(), openPathWithOS: vi.fn() } }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn() }))

import { CoderHeader } from './CoderHeader.jsx'
import { MissionChip } from './MissionToggle.jsx'
import { useMissionToggle } from './useMissionToggle.js'

const mission = (over = {}) => ({ on: false, locked: false, available: true, ...over })
const view = m => ({ conversationId: 'c1', mission: m })

function Harness({ id = 'c1', enabled = true, onToggle }) {
  const toggle = useMissionToggle(id, enabled)
  if (onToggle) onToggle(toggle)
  return <><MissionChip state={toggle.state} /><CoderHeader cwd="/work/app" mission={toggle} /></>
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getChatMissionToggle.mockResolvedValue(view(mission()))
})
afterEach(cleanup)

const sw = () => screen.findByRole('switch', { name: /mission/i })

describe('Mission toggle', () => {
  it('is off by default: unchecked, enabled, no chip', async () => {
    render(<Harness />)
    const s = await sw()
    expect(s).toHaveAttribute('aria-checked', 'false')
    expect(s).toBeEnabled()
    expect(screen.queryByTestId('mission-chip')).toBeNull()
  })

  it('turns on through the CLI, shows what it stored, and shows the Mission chip', async () => {
    api.setChatMissionToggle.mockResolvedValue(view(mission({ on: true })))
    render(<Harness />)
    fireEvent.click(await sw())
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'true'))
    expect(api.setChatMissionToggle).toHaveBeenCalledWith('c1', true)
    expect(screen.getByTestId('mission-chip')).toHaveTextContent('Mission')
  })

  it('turns off through the CLI', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mission({ on: true })))
    api.setChatMissionToggle.mockResolvedValue(view(mission()))
    render(<Harness />)
    const s = await sw()
    await waitFor(() => expect(s).toHaveAttribute('aria-checked', 'true'))
    fireEvent.click(s)
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'false'))
    expect(api.setChatMissionToggle).toHaveBeenCalledWith('c1', false)
    expect(screen.queryByTestId('mission-chip')).toBeNull()
  })

  const RUNNING = 'this chat is running a mission (session 7): stop or close that session first (`task session stop 7` or `task session close 7`; pausing keeps it open)'

  it("shows on and disabled, with the CLI's reason (session and how to stop it), while the chat is running a mission", async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mission({ on: true, locked: true, sessionId: 7, reason: RUNNING })))
    render(<Harness />)
    const s = await sw()
    await waitFor(() => expect(s).toHaveAttribute('aria-checked', 'true'))
    expect(s).toBeDisabled()
    expect(s).toHaveAttribute('title', RUNNING)
    const reason = screen.getByTestId('mission-toggle-reason')
    expect(reason).toHaveTextContent('session 7')
    expect(reason).toHaveTextContent('task session stop 7')
    expect(reason).toHaveAttribute('data-lock', 'running')
    expect(screen.getByTestId('mission-chip')).toHaveAttribute('title', 'This chat is running a mission')
    fireEvent.click(s)
    expect(api.setChatMissionToggle).not.toHaveBeenCalled()
  })

  it('names the session even when the CLI sent no reason', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mission({ on: true, locked: true, sessionId: 7 })))
    render(<Harness />)
    expect(await screen.findByTestId('mission-toggle-reason')).toHaveTextContent('Running a mission (session 7)')
  })

  it('does not call an unreadable mission state a running mission: its own reason, no session, no Mission chip', async () => {
    const reason = 'the mission state of this chat could not be read, so the toggle stays as it is: database is locked'
    api.getChatMissionToggle.mockResolvedValue(view(mission({ on: true, locked: true, reason })))
    render(<Harness />)
    const s = await sw()
    await waitFor(() => expect(s).toBeDisabled())
    const shown = screen.getByTestId('mission-toggle-reason')
    expect(shown).toHaveAttribute('data-lock', 'unreadable')
    expect(shown).toHaveTextContent('Mission state unknown')
    expect(shown).toHaveTextContent('could not be read')
    expect(shown).not.toHaveTextContent(/running a mission/i)
    expect(s).not.toHaveAttribute('title', 'This chat is running a mission')
    expect(screen.queryByTestId('mission-chip')).toBeNull()
  })

  it('is disabled with the reason below coding access', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mission({ available: false })))
    render(<Harness />)
    const s = await sw()
    await waitFor(() => expect(s).toBeDisabled())
    expect(screen.getByTestId('mission-toggle-reason')).toHaveTextContent('Missions need coding access or more')
    fireEvent.click(s)
    expect(api.setChatMissionToggle).not.toHaveBeenCalled()
  })

  it("shows the CLI's refusal inline and keeps the stored state", async () => {
    api.setChatMissionToggle.mockRejectedValue(new Error('this chat is running a mission (session 7)'))
    render(<Harness />)
    fireEvent.click(await sw())
    expect(await screen.findByTestId('mission-toggle-error')).toHaveTextContent('Could not change the mission setting: this chat is running a mission (session 7)')
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'false')
  })

  it('shows a load failure inline', async () => {
    api.getChatMissionToggle.mockRejectedValue(new Error('boom'))
    render(<Harness />)
    expect(await screen.findByTestId('mission-toggle-error')).toHaveTextContent('Could not read the mission setting')
  })

  it('is reachable by keyboard: a real button with a switch role, in tab order', async () => {
    render(<Harness />)
    const s = await sw()
    expect(s.tagName).toBe('BUTTON')
    expect(s).toHaveAttribute('type', 'button')
    expect(s.tabIndex).toBeGreaterThanOrEqual(0)
    s.focus()
    expect(s).toHaveFocus()
  })

  it('shows nothing for a CLI that does not know the toggle', async () => {
    api.getChatMissionToggle.mockResolvedValue({ conversationId: 'c1' })
    render(<Harness />)
    await waitFor(() => expect(api.getChatMissionToggle).toHaveBeenCalled())
    expect(screen.queryByRole('switch')).toBeNull()
  })

  it('keeps the choice made before the chat exists and applies it once, when the chat is made', async () => {
    api.setChatMissionToggle.mockResolvedValue(view(mission({ on: true })))
    let t
    render(<Harness id="" onToggle={x => { t = x }} />)
    fireEvent.click(await sw())
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'true'))
    expect(api.getChatMissionToggle).not.toHaveBeenCalled()
    expect(api.setChatMissionToggle).not.toHaveBeenCalled()
    await t.applyPending('new-conv')
    expect(api.setChatMissionToggle).toHaveBeenCalledTimes(1)
    expect(api.setChatMissionToggle).toHaveBeenCalledWith('new-conv', true)
    await t.applyPending('again') // applied once
    expect(api.setChatMissionToggle).toHaveBeenCalledTimes(1)
  })

  it('keeps the choice and rethrows when applying it fails, so the first message is not sent with the toggle off', async () => {
    api.setChatMissionToggle.mockRejectedValueOnce(new Error('database is locked')).mockResolvedValue(view(mission({ on: true })))
    let t
    render(<Harness id="" onToggle={x => { t = x }} />)
    fireEvent.click(await sw())
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'true'))
    await expect(t.applyPending('new-conv')).rejects.toThrow('Could not change the mission setting: database is locked')
    expect(await screen.findByTestId('mission-toggle-error')).toHaveTextContent('database is locked')
    await t.applyPending('new-conv') // the choice is still there: the retry applies it
    expect(api.setChatMissionToggle).toHaveBeenCalledTimes(2)
    expect(api.setChatMissionToggle).toHaveBeenLastCalledWith('new-conv', true)
    await t.applyPending('again') // and once it went through it is applied once
    expect(api.setChatMissionToggle).toHaveBeenCalledTimes(2)
  })

  it('keeps the locked switch legible (the default disabled 40% opacity is about 2:1 on this background), and only that one', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mission({ on: true, locked: true, sessionId: 7 })))
    const { unmount } = render(<Harness />)
    const sw = await screen.findByTestId('mission-toggle')
    await waitFor(() => expect(sw).toBeDisabled())
    expect(Number(sw.style.opacity)).toBeGreaterThanOrEqual(0.85)
    // The folder path wraps to its own row instead of being squeezed to two characters beside the reason.
    expect(screen.getByTestId('coder-cwd').style.flex).toBe('1 1 160px')
    unmount()
    api.getChatMissionToggle.mockResolvedValue(view(mission({ available: false })))
    render(<Harness />)
    const other = await screen.findByTestId('mission-toggle')
    expect(other.style.opacity).toBe('')
  })
})
