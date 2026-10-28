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

  it('shows on and disabled, with the reason, while the chat is running a mission', async () => {
    api.getChatMissionToggle.mockResolvedValue(view(mission({ on: true, locked: true, sessionId: 7 })))
    render(<Harness />)
    const s = await sw()
    await waitFor(() => expect(s).toHaveAttribute('aria-checked', 'true'))
    expect(s).toBeDisabled()
    expect(s).toHaveAttribute('title', 'This chat is running a mission')
    expect(screen.getByTestId('mission-toggle-reason')).toHaveTextContent('This chat is running a mission')
    expect(screen.getByTestId('mission-chip')).toHaveAttribute('title', 'This chat is running a mission')
    fireEvent.click(s)
    expect(api.setChatMissionToggle).not.toHaveBeenCalled()
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
})
