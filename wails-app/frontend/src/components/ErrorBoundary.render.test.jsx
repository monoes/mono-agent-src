// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import '../i18n.js'
import i18n from '../i18n.js'
import ErrorBoundary from './ErrorBoundary.jsx'

afterEach(async () => { cleanup(); await i18n.changeLanguage('en'); vi.restoreAllMocks() })

function Boom() { throw new Error('kaboom') }

describe('ErrorBoundary', () => {
  it('shows localized labels and copies the details', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    render(<ErrorBoundary><Boom /></ErrorBoundary>)
    expect(screen.getByText('Something went wrong')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Copy details' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    expect(writeText.mock.calls[0][0]).toContain('kaboom')
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument()
  })

  it('follows the language', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    await i18n.changeLanguage('es')
    render(<ErrorBoundary><Boom /></ErrorBoundary>)
    expect(screen.getByText('Algo salió mal')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Recargar la vista' })).toBeInTheDocument()
  })
})
