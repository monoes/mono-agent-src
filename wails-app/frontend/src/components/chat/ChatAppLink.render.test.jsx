// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({
  api: {
    openURL: vi.fn(), classifyChatPath: vi.fn(async () => 'refused'),
    getWorkflow: vi.fn(), getExecutionDetail: vi.fn(), listOrgDesigns: vi.fn(),
    getPersonDetail: vi.fn(), getProfileDocument: vi.fn(),
  },
  notify: vi.fn(),
}))
vi.mock('../../services/tasks.js', () => ({ tasksApi: { show: vi.fn() } }))
vi.mock('./chatLinks.js', async (orig) => ({ ...(await orig()), isUserGesture: vi.fn(() => true) }))
import { isUserGesture } from './chatLinks.js'
import { api, notify } from '../../services/api.js'
import { tasksApi } from '../../services/tasks.js'
import { ChatMarkdown } from './ChatMarkdown.jsx'

const UUID = '3f2b8c1e-9a4d-4e7b-8c55-0a1b2c3d4e5f'

afterEach(() => { cleanup(); vi.clearAllMocks(); isUserGesture.mockImplementation(() => true) })

describe('monoagent:// links in chat markdown', () => {
  it('renders a real workflow as a chip, with no host suffix, and navigates on click', async () => {
    api.getWorkflow.mockResolvedValue({ id: 'w1' })
    const nav = vi.fn()
    render(<ChatMarkdown content="[my flow](monoagent://workflow/w1)" onNavigate={nav} />)
    const chip = await screen.findByRole('button', { name: 'my flow' })
    expect(chip).toHaveAttribute('data-chat-app-link', 'workflow')
    expect(chip).toHaveAttribute('title', 'Open this workflow in the app')
    expect(document.body.textContent).not.toMatch(/\(.*\)/)
    fireEvent.click(chip)
    await waitFor(() => expect(nav).toHaveBeenCalledWith('noderunner', { workflowId: 'w1' }))
    expect(api.openURL).not.toHaveBeenCalled()
  })

  it('shows a forged id inert and never navigates', async () => {
    api.getWorkflow.mockResolvedValue(null)
    const nav = vi.fn()
    render(<ChatMarkdown content="[ghost](monoagent://workflow/nope)" onNavigate={nav} />)
    await waitFor(() => expect(screen.getByText('ghost')).toHaveAttribute('title', 'This link points to something that does not exist'))
    expect(screen.queryByRole('button')).toBeNull()
    fireEvent.click(screen.getByText('ghost'))
    expect(nav).not.toHaveBeenCalled()
  })

  it('ignores a click the user did not make', async () => {
    api.getWorkflow.mockResolvedValue({ id: 'w1' })
    const nav = vi.fn()
    render(<ChatMarkdown content="[f](monoagent://workflow/w1)" onNavigate={nav} />)
    const chip = await screen.findByRole('button', { name: 'f' })
    isUserGesture.mockImplementation(() => false)
    fireEvent.click(chip)
    await new Promise(r => setTimeout(r, 20))
    expect(nav).not.toHaveBeenCalled()
    expect(api.getWorkflow).toHaveBeenCalledTimes(1)
  })

  it('revalidates at click time and refuses when the target is gone', async () => {
    api.getWorkflow.mockResolvedValueOnce({ id: 'w1' }).mockResolvedValue(null)
    const nav = vi.fn()
    render(<ChatMarkdown content="[f](monoagent://workflow/w1)" onNavigate={nav} />)
    fireEvent.click(await screen.findByRole('button', { name: 'f' }))
    await waitFor(() => expect(notify).toHaveBeenCalled())
    expect(nav).not.toHaveBeenCalled()
  })

  it('settings links need no lookup', async () => {
    const nav = vi.fn()
    render(<ChatMarkdown content="[health](monoagent://settings/health)" onNavigate={nav} />)
    fireEvent.click(await screen.findByRole('button', { name: 'health' }))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('settings', { section: 'health' }))
  })

  it('task links check the board', async () => {
    tasksApi.show.mockResolvedValue({ task: { id: 7 } })
    const nav = vi.fn()
    render(<ChatMarkdown content="[t7](monoagent://task/7)" onNavigate={nav} />)
    fireEvent.click(await screen.findByRole('button', { name: 't7' }))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('tasks', null))
    expect(tasksApi.show).toHaveBeenCalledWith(7)
  })

  it('stays inert without onNavigate, without any lookup', async () => {
    render(<ChatMarkdown content="[f](monoagent://workflow/w1)" />)
    expect(screen.queryByRole('button')).toBeNull()
    expect(api.getWorkflow).not.toHaveBeenCalled()
  })

  it.each([
    '[x](monoagent://bogus/1)', '[x](monoagent://workflow/a/b)', '[x](monoagent://settings/evil)',
    '[x](MONOAGENT://task/1)', '[x](monoagent:task/1)',
  ])('renders %s inert', async (md) => {
    render(<ChatMarkdown content={md} onNavigate={vi.fn()} />)
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByRole('link')).toBeNull()
    expect(api.getWorkflow).not.toHaveBeenCalled()
  })

  it('respects the per-message lookup budget', async () => {
    api.getWorkflow.mockResolvedValue({ id: 'w1' })
    const md = Array.from({ length: 35 }, (_, i) => `[l${i}](monoagent://workflow/w1)`).join(' ')
    render(<ChatMarkdown content={md} onNavigate={vi.fn()} />)
    await waitFor(() => expect(api.getWorkflow).toHaveBeenCalledTimes(30))
    await new Promise(r => setTimeout(r, 20))
    expect(api.getWorkflow).toHaveBeenCalledTimes(30)
  })

  it('does not change how https links render', () => {
    render(<ChatMarkdown content="[the report](https://example.com/r)" onNavigate={vi.fn()} />)
    expect(screen.getByRole('link', { name: 'the report' })).toHaveAttribute('title', 'Open in browser: https://example.com/r')
  })
})
