// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act } from '@testing-library/react'

const api = vi.hoisted(() => ({
  showPlatform: vi.fn(),
  getOAuthCredentials: vi.fn(() => Promise.resolve('')),
  connectPlatformOAuth: vi.fn(), cancelPlatformOAuth: vi.fn(), setOAuthCredentials: vi.fn(),
  saveConnectionDirect: vi.fn(), openURL: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api,
  onConnectionProgress: () => () => {},
  onConnectionDone: () => () => {},
}))

import ApiConnectionModal from './ApiConnectionModal.jsx'

Element.prototype.scrollIntoView = () => {}
afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers() })

const platform = { id: 'linear', name: 'Linear', methods: ['apikey'], fields: { apikey: [{ key: 'api_key', label: 'API key' }] } }

describe('ApiConnectionModal load failures', () => {
  it('shows an error instead of an unhandled rejection when the platform cannot be loaded', async () => {
    api.showPlatform.mockRejectedValue(new Error('cli exploded'))
    render(<ApiConnectionModal platform={platform} conn={null} onClose={() => {}} onRefresh={() => Promise.resolve()} onDisconnect={() => {}} />)
    expect(await screen.findByText(/cli exploded/)).toBeInTheDocument()
  })

  it('does not close a later modal with the timer of an unmounted one', async () => {
    vi.useFakeTimers()
    api.showPlatform.mockResolvedValue({})
    api.saveConnectionDirect.mockResolvedValue('ok:saved')
    const onClose = vi.fn()
    const { unmount } = render(<ApiConnectionModal platform={platform} conn={null} onClose={onClose} onRefresh={() => Promise.resolve()} onDisconnect={() => {}} />)
    await act(async () => {})
    const btn = screen.getAllByRole('button').find(b => /connect|save/i.test(b.textContent))
    await act(async () => { btn.click() })
    unmount()
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(onClose).not.toHaveBeenCalled()
  })
})
