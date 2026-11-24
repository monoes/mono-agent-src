// @vitest-environment jsdom
// W4 R21-17: a failed tag read ends the spinner and says so; a failed tag write says so.
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const { mockApi } = vi.hoisted(() => ({
  mockApi: {
    getPersonTags: vi.fn(), getAllTags: vi.fn(), addPersonTag: vi.fn(), removePersonTag: vi.fn(), updateTagColor: vi.fn(),
    getPeopleTagsMap: vi.fn(() => Promise.resolve({ p1: [{ id: 't1', name: 'Lead', color: '#00b4d8' }] })),
    getPeople: vi.fn(() => Promise.resolve([
      { id: 'p1', full_name: 'Alice Founder', username: 'alice', platform: 'LINKEDIN', platform_user_id: 'a', tags: [] },
    ])),
    getPeopleCount: vi.fn(() => Promise.resolve(1)),
    getAllPersonMessages: vi.fn(() => Promise.resolve([])),
  },
}))
vi.mock('../services/api.js', () => ({ api: mockApi, subscribeEvent: () => () => {}, notify: vi.fn() }))
vi.mock('../wailsjs/go/main/App', () => ({}))
import People from './People.jsx'

afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('People tag editor errors', { timeout: 20000 }, () => {
  it('stops loading and shows the error when the tag read fails', async () => {
    mockApi.getPersonTags.mockRejectedValue(new Error('tags down'))
    mockApi.getAllTags.mockResolvedValue([])
    render(<People />)
    await screen.findByText('@alice', {}, { timeout: 5000 })
    fireEvent.click(screen.getAllByTitle('Click to manage tags')[0])
    expect(await screen.findByTestId('tag-error', {}, { timeout: 5000 })).toHaveTextContent('tags down')
    await waitFor(() => expect(screen.queryByText(/Loading tags/)).toBeNull())
  })
})
