// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'

const isReady = vi.hoisted(() => vi.fn())
vi.mock('../services/api.js', () => ({ api: { isReady } }))

import StartupOverlay from './StartupOverlay.jsx'

afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('StartupOverlay', () => {
  it('blocks until the backend is ready, then goes away', async () => {
    isReady.mockResolvedValueOnce(false).mockResolvedValue(true)
    render(<StartupOverlay />)
    expect(screen.getByRole('status')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull())
  })
})
