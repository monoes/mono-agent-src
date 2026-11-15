// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const { mockApi } = vi.hoisted(() => ({
  mockApi: {
    getPeople: vi.fn(),
    getPeopleCount: vi.fn(),
    getPeopleTagsMap: vi.fn(() => Promise.resolve({})),
  },
}))

vi.mock('../services/api.js', () => ({ api: mockApi, subscribeEvent: () => () => {}, notify: vi.fn() }))
vi.mock('../wailsjs/go/main/App', () => ({}))

import People from './People.jsx'

describe('People load failure', { timeout: 20000 }, () => {
  afterEach(cleanup)
  beforeEach(() => {
    vi.clearAllMocks()
    mockApi.getPeopleCount.mockResolvedValue(1)
  })

  it('shows the error and a Retry that reloads, with the spinner cleared', async () => {
    mockApi.getPeople.mockRejectedValueOnce(new Error('database is locked'))
    mockApi.getPeople.mockResolvedValue([{ id: 'a', username: 'ada', platform: 'X' }])
    const { container } = render(<People />)
    expect(await screen.findByText('database is locked', {}, { timeout: 5000 })).toBeInTheDocument()
    expect(container.querySelector('.spinner')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByLabelText('Select @ada', {}, { timeout: 5000 })).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText('database is locked')).toBeNull())
  })
})
