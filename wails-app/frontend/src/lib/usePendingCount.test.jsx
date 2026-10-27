// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { renderHook, waitFor, cleanup } from '@testing-library/react'
vi.mock('../services/api.js', () => ({ api: { getSummarySections: vi.fn() } }))
vi.mock('./usePageVisible.js', () => ({ usePageVisibleRef: () => ({ current: true }), useVisibleCatchUp: () => {} }))
import { api } from '../services/api.js'
import { usePendingCount, pendingFromSummary } from './usePendingCount.js'

afterEach(cleanup)
beforeEach(() => api.getSummarySections.mockReset())

describe('pendingFromSummary', () => {
  it('adds workflow approvals, leads and drafts; unknown on error', () => {
    expect(pendingFromSummary({ hil: { workflow_pending: 2, people_review: 3, drafts: 1, link_suggestions: 9 } })).toBe(6)
    expect(pendingFromSummary({ hil: { error: 'db locked' } })).toBeNull()
    expect(pendingFromSummary(null)).toBeNull()
  })
})

describe('usePendingCount', () => {
  it('reads the Jev-free hil section of summary', async () => {
    api.getSummarySections.mockResolvedValue({ v: 1, hil: { workflow_pending: 1, people_review: 1, drafts: 0 } })
    const { result } = renderHook(() => usePendingCount())
    await waitFor(() => expect(result.current.count).toBe(2))
    expect(api.getSummarySections).toHaveBeenCalledWith('hil')
  })
  it('notifies, with no question text, when a coder question starts waiting for the person', async () => {
    const shown = []
    globalThis.Notification = class { constructor(title, o) { shown.push([title, o.body]) } static permission = 'granted' }
    try {
      api.getSummarySections.mockResolvedValueOnce({ hil: { coder_items: {} } }).mockResolvedValueOnce({ hil: { coder_items: { parked_question: 1 } } })
      const { result } = renderHook(() => usePendingCount())
      await waitFor(() => expect(api.getSummarySections).toHaveBeenCalledTimes(1))
      await result.current.reload()
      await waitFor(() => expect(shown).toHaveLength(1))
      expect(shown[0][0]).toBe('Coder session')
    } finally {
      delete globalThis.Notification
    }
  })
  it('notifies when the count goes up', async () => {
    const shown = []
    globalThis.Notification = class { constructor(title, o) { shown.push(o.body) } static permission = 'granted' }
    try {
      api.getSummarySections.mockResolvedValueOnce({ hil: { workflow_pending: 1 } }).mockResolvedValueOnce({ hil: { workflow_pending: 3 } })
      const { result } = renderHook(() => usePendingCount())
      await waitFor(() => expect(result.current.count).toBe(1))
      await result.current.reload()
      await waitFor(() => expect(result.current.count).toBe(3))
      expect(shown).toEqual(['2 new items are waiting for your review'])
    } finally {
      delete globalThis.Notification
    }
  })
})
