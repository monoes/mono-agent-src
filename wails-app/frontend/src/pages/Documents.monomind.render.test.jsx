// @vitest-environment jsdom
// The Documents page's monomind banner stays true: it goes when any run of
// the set-up finishes, and it is read again when the page becomes active (a
// profile switch while it was hidden).
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup, act } from '@testing-library/react'

const { events } = vi.hoisted(() => ({ events: {} }))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))
vi.mock('../wailsjs/go/main/App', () => ({
  ListProfileDocuments: vi.fn(() => Promise.resolve([])),
  ListExcludedDocuments: vi.fn(() => Promise.resolve([])),
  IsMonomindInitialized: vi.fn(),
  InitializeMonomindProfile: vi.fn(),
}))
vi.mock('../services/api.js', async importOriginal => ({
  ...(await importOriginal()),
  onMonomindInitEvent: (cb) => {
    (events.init ||= new Set()).add(cb)
    return () => events.init?.delete(cb)
  },
}))

import Documents from './Documents.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

const BANNER = /This profile isn't set up with monomind yet/
const emit = (payload) => act(() => { for (const cb of [...(events.init || [])]) cb(payload) })

beforeEach(() => {
  vi.clearAllMocks()
  for (const k of Object.keys(events)) delete events[k]
})
afterEach(cleanup)

describe('Documents monomind banner', () => {
  it('goes away when a set-up started elsewhere finishes', async () => {
    WailsApp.IsMonomindInitialized.mockResolvedValue(false)
    render(<Documents />)
    expect(await screen.findByText(BANNER)).toBeInTheDocument()
    WailsApp.IsMonomindInitialized.mockResolvedValue(true)
    // e.g. the Agents page's prompt or the onboarding ran it; this page did not start it
    emit({ kind: 'line', message: 'writing .monomind/config.yaml' })
    expect(screen.getByText(BANNER)).toBeInTheDocument()
    emit({ kind: 'done', message: '' })
    await waitFor(() => expect(screen.queryByText(BANNER)).not.toBeInTheDocument())
  })

  it('reflects the profile selected while the page was hidden', async () => {
    WailsApp.IsMonomindInitialized.mockResolvedValue(true)
    const view = render(<Documents isActive />)
    await waitFor(() => expect(WailsApp.IsMonomindInitialized).toHaveBeenCalledTimes(1))
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument()
    view.rerender(<Documents isActive={false} />)
    // another profile, never set up, is selected now
    WailsApp.IsMonomindInitialized.mockResolvedValue(false)
    view.rerender(<Documents isActive />)
    expect(await screen.findByText(BANNER)).toBeInTheDocument()
    expect(WailsApp.IsMonomindInitialized).toHaveBeenCalledTimes(2)
  })
})
