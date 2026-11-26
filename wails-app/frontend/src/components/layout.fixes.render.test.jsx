// @vitest-environment jsdom
import React, { useState } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const { App, aitaskApi, budgetApi } = vi.hoisted(() => ({
  App: { GetApplicationAssets: vi.fn(), GetApplicationTab: vi.fn(), OpenURL: vi.fn() },
  aitaskApi: { status: vi.fn(), submit: vi.fn(), startFill: vi.fn(), get: vi.fn(), cancel: vi.fn() },
  budgetApi: { list: vi.fn(), set: vi.fn(), clear: vi.fn(), forTask: vi.fn() },
}))
vi.mock('../wailsjs/go/main/App', () => App)
vi.mock('./SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi }))
vi.mock('./settings/aitaskBudgetApi.js', async (orig) => ({ ...(await orig()), aitaskBudgetApi: budgetApi }))
vi.mock('../services/api.js', () => ({ api: {}, notify: vi.fn(), onChatEvent: () => () => {}, onOrgEvent: () => () => {} }))
vi.mock('../lib/orgEventStreams.js', () => ({ acquireOrgEvents: () => () => {} }))

import ApplicationDetailModal from './applications/ApplicationDetailModal.jsx'
import ApplicationFillSection from './applications/ApplicationFillSection.jsx'
import AiTaskBudgetsSection, { PLAIN_DECIMAL } from './settings/AiTaskBudgetsSection.jsx'
import '../i18n.js'
import { BubbleDock } from './bubbles/BubbleDock.jsx'
import { isModalOpenAbove } from './bubbles/modalAbove.js'
import { CoderBubbleContext } from './bubbles/CoderBubbleContext.jsx'
import { mockApplicationDetail, mockApplicationAssets } from './applications/fixtures.js'

beforeEach(() => {
  vi.clearAllMocks()
  App.GetApplicationAssets.mockResolvedValue(mockApplicationAssets)
  App.GetApplicationTab.mockResolvedValue({ tab: null, submitObservedAt: '' })
  aitaskApi.status.mockResolvedValue({ enabled: false, reason: 'Coder mode is off. It runs a coding agent with access to a folder, so only you can turn it on, in Settings.' })
})
afterEach(cleanup)

const css = (name) => readFileSync(join(__dirname, name), 'utf8')

describe('1: the coder-off note does not break the detail header', () => {
  it('the note wraps onto its own line instead of squeezing the title, and the Settings link is in it', async () => {
    render(
      <CoderBubbleContext.Provider value={{ openDraft: vi.fn(), navigate: vi.fn() }}>
        <ApplicationDetailModal applicationId="app-test-1" detail={{ ...mockApplicationDetail, status: 'pending' }} onClose={() => {}} />
      </CoderBubbleContext.Provider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Apply with coder' }))
    const note = (await screen.findByText(/Coder mode is off\./)).closest('[role=status]')
    expect(note).toHaveTextContent('Coder mode is off.')
    expect(note.style.whiteSpace).toBe('normal')
    expect(note.style.flex).toBe('1 0 100%')
    expect(note.style.overflowWrap).toBe('anywhere')
    expect(note.parentElement.style.display).toBe('contents') // the note is a flex item of the wrapping button row
    expect(screen.getByText('Open Settings')).toBeInTheDocument()
    const row = note.parentElement.parentElement
    expect(row.style.flexWrap).toBe('wrap')
    expect(row.style.flexShrink).not.toBe('0')
  })
})

describe('2: a bubble opened from a dialog stays on top of it', () => {
  it('the dock, backdrop and overlay sit above the pages\' dialogs (z 9000-9500)', () => {
    const z = (sel) => Number(new RegExp(`${sel.replace('.', '\\.')} \\{[^}]*?z-index: (\\d+)`).exec(css('bubbles/bubbles.css'))[1])
    expect(z('.bubble-dock')).toBeGreaterThan(9500)
    expect(z('.bubble-backdrop')).toBeGreaterThan(9500)
    expect(z('.bubble-overlay')).toBeGreaterThan(z('.bubble-backdrop'))
  })

  it('a dialog underneath the overlay does not block Escape/backdrop; one above it does', () => {
    const layer = (zIndex) => {
      const wrap = document.createElement('div')
      wrap.style.position = 'fixed'; wrap.style.zIndex = String(zIndex)
      wrap.innerHTML = '<div role="dialog" aria-modal="true"></div>'
      document.body.appendChild(wrap)
      return wrap
    }
    const page = layer(9000)
    expect(isModalOpenAbove()).toBe(false)
    const confirm = layer(10001)
    expect(isModalOpenAbove()).toBe(true)
    page.remove(); confirm.remove()
  })
})

describe('3: narrow layouts', () => {
  it('the header row may wrap and shrink', async () => {
    render(<ApplicationDetailModal applicationId="x" detail={{ ...mockApplicationDetail, status: 'pending' }} onClose={() => {}} />)
    const banner = await screen.findByRole('banner')
    const top = banner.firstElementChild
    expect(top.style.flexWrap).toBe('wrap')
    expect(top.firstElementChild.style.flex).toBe('1 1 240px')
  })

  it('the filled-form report stacks label over value below 560px', () => {
    const { container } = render(<ApplicationFillSection fill={mockApplicationAssets.fill} />)
    const rule = container.querySelector('style').textContent
    expect(rule).toMatch(/@media \(max-width: 560px\)/)
    expect(rule).toMatch(/\.fill-report td \{ display: block/)
    expect(container.querySelector('table.fill-report')).not.toBeNull()
  })
})

describe('5: the detail dialog takes and traps focus', () => {
  function Page({ onClose = () => {} }) {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button data-testid="opener">open</button>
        {open && <ApplicationDetailModal applicationId="x" detail={{ ...mockApplicationDetail, status: 'pending' }} onClose={() => { setOpen(false); onClose() }} />}
      </>
    )
  }

  it('is a labelled modal dialog and moves focus into itself, restoring it on close', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const { unmount } = render(<ApplicationDetailModal applicationId="x" detail={{ ...mockApplicationDetail, status: 'pending' }} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: /Application detail: Lead Systems Architect/ })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAttribute('aria-labelledby', 'application-detail-label')
    expect(dialog).toContainElement(document.activeElement)
    unmount()
    expect(document.activeElement).toBe(opener)
    opener.remove()
  })

  it('Tab wraps from the last control to the first, and Shift+Tab the other way', async () => {
    render(<Page />)
    const dialog = await screen.findByRole('dialog')
    const items = [...dialog.querySelectorAll('button, a[href], input, select, textarea')].filter(el => !el.disabled)
    const first = items[0]; const last = items[items.length - 1]
    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(first)
    first.focus()
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
  })

  it('Escape closes it', async () => {
    const onClose = vi.fn()
    render(<Page onClose={onClose} />)
    await screen.findByRole('dialog')
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})

describe('6: the dock passes keys directly', () => {
  it('renders bubbles without the spread-key warning', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const bubbles = [{ key: 'c1', conversationId: 'c1', cwd: '/x', model: '', runtime: '' }, { key: 'c2', conversationId: 'c2', cwd: '/y', model: '', runtime: '' }]
    const store = {
      bubbles, side: 'right', expandedKey: '', summaryOf: () => ({ status: 'idle', unread: 0, costByTurn: {} }), reorder: () => {}, openDraft: () => {},
    }
    render(<BubbleDock store={store} onOpen={() => {}} onClose={() => {}} registerEl={() => {}} titleOf={x => x} />)
    expect(spy.mock.calls.flat().join(' ')).not.toMatch(/key.*spread|spread.*key/i)
    spy.mockRestore()
  })
})

describe('7: budget inputs', () => {
  beforeEach(() => {
    budgetApi.list.mockResolvedValue({ operatorUsd: 5, spentUsd: 0, caps: [] })
    budgetApi.set.mockResolvedValue({})
  })

  it('accepts plain decimals only', () => {
    for (const ok of ['0', '5', '1.5', '.5', '2.']) expect(PLAIN_DECIMAL.test(ok), ok).toBe(true)
    for (const bad of ['1e2', '0x10', '-1', '1,5', 'Infinity', '', '1 2']) expect(PLAIN_DECIMAL.test(bad), bad).toBe(false)
  })

  it('1e2 does not enable Set cap, and the global cap row has a visible label', async () => {
    render(<AiTaskBudgetsSection />)
    const input = await screen.findByLabelText('Global cap in USD')
    expect(screen.getByText('Cap for all tasks (USD)')).toBeVisible()
    const btn = screen.getByRole('button', { name: 'Set cap' })
    fireEvent.change(input, { target: { value: '1e2' } })
    expect(btn).toBeDisabled()
    fireEvent.change(input, { target: { value: '12.5' } })
    expect(btn).toBeEnabled()
    fireEvent.click(btn)
    await waitFor(() => expect(budgetApi.set).toHaveBeenCalledWith('global', '', 12.5))
  })
})
