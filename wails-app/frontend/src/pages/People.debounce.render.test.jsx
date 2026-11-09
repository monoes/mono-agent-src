// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, cleanup, fireEvent, act } from '@testing-library/react'

const { mockApi } = vi.hoisted(() => ({
  mockApi: {
    getPeople: vi.fn(() => Promise.resolve([])),
    getPeopleCount: vi.fn(() => Promise.resolve(0)),
    getPeopleTagsMap: vi.fn(() => Promise.resolve({})),
  },
}))

vi.mock('../services/api.js', () => ({ api: mockApi, subscribeEvent: () => () => {}, notify: vi.fn() }))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))
vi.mock('../wailsjs/go/main/App', () => ({}))

import People from './People.jsx'

describe('People search debounce', () => {
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('leaves no debounce timer pending after unmount (a late timer fires after jsdom teardown)', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { container, unmount } = render(<People />)
    const box = container.querySelector('input[type="text"], input:not([type])')
    fireEvent.change(box, { target: { value: 'ada' } })
    expect(vi.getTimerCount()).toBeGreaterThan(0) // the debounce is pending
    unmount()
    expect(vi.getTimerCount()).toBe(0) // nothing outlives the component
    await act(async () => { vi.advanceTimersByTime(1000) })
  })
})
