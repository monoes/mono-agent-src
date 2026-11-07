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
import { showBossInAgents, cancelShowBoss } from '../../lib/showBoss.js'
import { CoderBubbleContext } from '../bubbles/CoderBubbleContext.jsx'
import { CoderHeader } from '../chat/CoderHeader.jsx'
import { OpenRunNotice } from '../chat/OpenRunNotice.jsx'
import ControlRoom from '../../pages/ControlRoom/ControlRoom.jsx'

const { sessionsApi } = vi.hoisted(() => ({ sessionsApi: { list: vi.fn() } }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi }))

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

describe('the wait is cancellable', () => {
  afterEach(() => { document.body.innerHTML = ''; cancelShowBoss() })
  const section = () => { const el = document.createElement('section'); el.setAttribute('data-testid', 'open-run-boss'); el.tabIndex = -1; el.scrollIntoView = vi.fn(); return el }
  it('a newer call cancels the one before it: only the newer one focuses', async () => {
    const first = showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 500 })
    const second = showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 500 })
    const el = section(); setTimeout(() => document.body.appendChild(el), 30)
    expect(await first).toBe(false)
    expect(await second).toBe(true)
    expect(el.scrollIntoView).toHaveBeenCalledTimes(1)
  })
  it('a key press or a click while it waits cancels it: no focus after the person moved on', async () => {
    for (const ev of ['keydown', 'pointerdown']) {
      const p = showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 500 })
      await new Promise(r => setTimeout(r, 10)) // the click that started it is over
      document.dispatchEvent(new Event(ev, { bubbles: true }))
      const el = section(); document.body.appendChild(el)
      expect(await p).toBe(false)
      expect(el.scrollIntoView).not.toHaveBeenCalled()
      expect(document.activeElement).not.toBe(el)
      document.body.innerHTML = ''
    }
  })
  it('the click that starts it does not cancel it', async () => {
    const el = section(); setTimeout(() => document.body.appendChild(el), 20)
    const p = showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 500 })
    document.dispatchEvent(new Event('pointerdown', { bubbles: true })) // the same click, still bubbling
    expect(await p).toBe(true)
  })
})

describe('no listener is left behind', () => {
  afterEach(() => { vi.restoreAllMocks(); document.body.innerHTML = ''; cancelShowBoss() })
  const track = () => {
    const live = new Map()
    const key = (t, fn, c) => `${t}|${fn?.name}|${!!(c === true || c?.capture)}`
    const add = vi.spyOn(document, 'addEventListener').mockImplementation((t, fn, c) => { const k = key(t, fn, c); live.set(k, (live.get(k) || 0) + 1) })
    const rem = vi.spyOn(document, 'removeEventListener').mockImplementation((t, fn, c) => { const k = key(t, fn, c); live.set(k, (live.get(k) || 0) - 1) })
    return { live: () => [...live.values()].reduce((a, b) => a + b, 0), adds: () => add.mock.calls.length, removes: () => rem.mock.calls.length }
  }
  it('the section already on the page: the wait ends at once and registers nothing that stays', async () => {
    const el = document.createElement('section'); el.setAttribute('data-testid', 'open-run-boss'); el.tabIndex = -1; document.body.appendChild(el)
    const t = track()
    for (let i = 0; i < 3; i++) expect(await showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 100 })).toBe(true)
    await new Promise(r => setTimeout(r, 20)) // past the zero-delay registration
    expect(t.live()).toBe(0)
    expect(t.adds()).toBe(t.removes())
  })
  it('a wait that runs out or is cancelled leaves none either', async () => {
    const t = track()
    expect(await showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 40 })).toBe(false)
    const p = showBossInAgents(vi.fn(), { intervalMs: 5, timeoutMs: 500 })
    await new Promise(r => setTimeout(r, 10))
    cancelShowBoss()
    expect(await p).toBe(false)
    await new Promise(r => setTimeout(r, 20))
    expect(t.live()).toBe(0)
    expect(t.adds()).toBe(t.removes())
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
  it('is not offered when no boss session is known (show false)', () => {
    render(<ShowBossLink navigate={vi.fn()} show={false} />)
    expect(screen.queryByTestId('show-boss-link')).toBeNull()
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
    const boss = { id: 7, boss: true, state: 'running', archived: false }
    beforeEach(() => { sessionsApi.list.mockReset(); sessionsApi.list.mockResolvedValue({ sessions: [boss] }) })
    it('shows it while the run is active and a boss session is known, and it goes to the Agents page', async () => {
      const navigate = vi.fn()
      mount(golden('overview_running'), navigate)
      fireEvent.click(await screen.findByRole('button', { name: LINK }))
      expect(navigate).toHaveBeenCalledWith('ai')
    })
    it('is absent when the run is active but no boss session is known (a terminal start without --boss-session)', async () => {
      sessionsApi.list.mockResolvedValue({ sessions: [] })
      mount(golden('overview_running'), vi.fn())
      await screen.findByTestId('cr-tree')
      await waitFor(() => expect(sessionsApi.list).toHaveBeenCalled())
      expect(screen.queryByTestId('show-boss-link')).toBeNull()
    })
    it('is absent for a finished run', async () => {
      const done = JSON.parse(JSON.stringify(golden('overview_running'))); done.run.active = false
      mount(done, vi.fn())
      await screen.findByTestId('cr-tree')
      expect(screen.queryByTestId('show-boss-link')).toBeNull()
      expect(sessionsApi.list).not.toHaveBeenCalled() // nothing is read for a run that is not going
    })
    it('is absent under an error reading and with no run', async () => {
      mount(golden('overview_error'), vi.fn())
      await screen.findByTestId('cr-error')
      expect(screen.queryByTestId('show-boss-link')).toBeNull()
      cleanup()
      mount(golden('overview_no_run'), vi.fn())
      await screen.findByTestId('cr-no-run')
      expect(screen.queryByTestId('show-boss-link')).toBeNull()
    })
  })
})
