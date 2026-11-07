// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import '../../i18n.js'
import ShowBossLink from './ShowBossLink.jsx'
import { showBossInAgents } from '../../lib/showBoss.js'
import { CoderBubbleContext } from '../bubbles/CoderBubbleContext.jsx'
import { CoderHeader } from '../chat/CoderHeader.jsx'
import { OpenRunNotice } from '../chat/OpenRunNotice.jsx'
import ControlRoom from '../../pages/ControlRoom/ControlRoom.jsx'

// The open run's boss is reached from the chat header, the first-turn notice and the Control room: a link that goes to the Agents
// page and brings the boss section into view. The page is not in the sidebar, so this is how it is found.
afterEach(cleanup)
const LINK = 'Show the boss in AI agents'

describe('showBossInAgents', () => {
  afterEach(() => { document.body.innerHTML = '' })
  it('navigates to the Agents page, waits for the boss section to mount, scrolls to it and focuses it', async () => {
    const navigate = vi.fn()
    const el = document.createElement('section'); el.setAttribute('data-testid', 'open-run-boss'); el.tabIndex = -1
    el.scrollIntoView = vi.fn()
    const p = showBossInAgents(navigate, { intervalMs: 5, timeoutMs: 500 })
    expect(navigate).toHaveBeenCalledWith('ai')
    setTimeout(() => document.body.appendChild(el), 30) // the page mounts a moment later
    expect(await p).toBe(true)
    expect(el.scrollIntoView).toHaveBeenCalledWith({ block: 'center' })
    expect(document.activeElement).toBe(el)
  })
  it('gives up (false) when the section never mounts, and does nothing without a navigate', async () => {
    expect(await showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 40 })).toBe(false)
    expect(await showBossInAgents(null)).toBe(false)
  })
})

describe('the link', () => {
  it('runs before, then navigates to the Agents page', async () => {
    const navigate = vi.fn(), before = vi.fn()
    render(<ShowBossLink navigate={navigate} before={before} />)
    fireEvent.click(screen.getByRole('button', { name: LINK }))
    expect(before).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('ai'))
  })
  it('uses the app navigation from the context, and is absent where there is none', () => {
    const navigate = vi.fn()
    const { unmount } = render(<CoderBubbleContext.Provider value={{ navigate }}><ShowBossLink /></CoderBubbleContext.Provider>)
    fireEvent.click(screen.getByTestId('show-boss-link'))
    expect(navigate).toHaveBeenCalledWith('ai')
    unmount()
    render(<ShowBossLink />)
    expect(screen.queryByTestId('show-boss-link')).toBeNull()
  })
})

describe('where it is offered', () => {
  const chatMode = { state: { mode: 'open', locked: true, reason: 'locked', options: [] }, busy: false, error: '', set() {}, checking: false, checkOpen() {} }
  it('the chat header shows it while a run exists, not otherwise', () => {
    const navigate = vi.fn()
    const { unmount } = render(<CoderHeader cwd="/w" chatMode={chatMode} openEnded={{ runExists: true, conversationId: 'c1', onShowBoss: { navigate } }} />)
    fireEvent.click(screen.getByRole('button', { name: LINK }))
    expect(navigate).toHaveBeenCalledWith('ai')
    unmount()
    render(<CoderHeader cwd="/w" chatMode={chatMode} openEnded={{ runExists: false, conversationId: 'c1', onShowBoss: { navigate } }} />)
    expect(screen.queryByTestId('show-boss-link')).toBeNull()
  })
  it('the first-turn notice has it', () => {
    const navigate = vi.fn()
    render(<OpenRunNotice onDismiss={() => {}} navigate={navigate} />)
    fireEvent.click(screen.getByRole('button', { name: LINK }))
    expect(navigate).toHaveBeenCalledWith('ai')
  })
  describe('the Control room', () => {
    const here = dirname(fileURLToPath(import.meta.url))
    const golden = n => JSON.parse(readFileSync(join(here, '../../../../../internal/cli/doomsday/testdata', `controlroom_${n}.golden.json`), 'utf8'))
    let api
    beforeEach(() => { api = { overview: vi.fn(), feed: vi.fn().mockResolvedValue(golden('feed_page')), stop: vi.fn() } })
    const mount = (doc, navigate) => { api.overview.mockResolvedValue(doc); return render(<CoderBubbleContext.Provider value={{ navigate }}><ControlRoom api={api} /></CoderBubbleContext.Provider>) }
    it('shows it while the run is active and it goes to the Agents page', async () => {
      const navigate = vi.fn()
      mount(golden('overview_running'), navigate)
      fireEvent.click(await screen.findByRole('button', { name: LINK }))
      expect(navigate).toHaveBeenCalledWith('ai')
    })
    it('is absent when there is no run', async () => {
      mount(golden('overview_no_run'), vi.fn())
      await screen.findByTestId('cr-no-run')
      expect(screen.queryByTestId('show-boss-link')).toBeNull()
    })
  })
})
