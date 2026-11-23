// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../i18n.js'
import MonomindInitPrompt from './MonomindInitPrompt.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { onApiError } from '../services/api.js'

vi.mock('../wailsjs/go/main/App', () => ({
  InitializeMonomindProfile: vi.fn(),
}))

beforeEach(() => vi.clearAllMocks())
afterEach(() => cleanup())

describe('MonomindInitPrompt', () => {
  it('leaves the spinner with the refusal when signed out (no error event is emitted)', async () => {
    WailsApp.InitializeMonomindProfile.mockResolvedValue(JSON.stringify({ error: 'sign in to continue', login_required: true }))
    const errs = []
    const off = onApiError(e => errs.push(e))
    render(<MonomindInitPrompt />)
    fireEvent.click(screen.getByRole('button', { name: /Set up agents/ }))
    await waitFor(() => expect(errs.some(e => /sign in/i.test(e.message))).toBe(true))
    expect(document.querySelector('.spinner')).toBeNull()
    expect(screen.getByRole('button', { name: /Set up agents/ })).toBeInTheDocument()
    off && off()
  })

  it('leaves the spinner when the binding throws', async () => {
    WailsApp.InitializeMonomindProfile.mockRejectedValue(new Error('boom'))
    render(<MonomindInitPrompt />)
    fireEvent.click(screen.getByRole('button', { name: /Set up agents/ }))
    expect(await screen.findByRole('button', { name: /Set up agents/ })).toBeInTheDocument()
    expect(document.querySelector('.spinner')).toBeNull()
  })
})
