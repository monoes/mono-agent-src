// @vitest-environment jsdom
// W4 r20-09: a late answer for an earlier row must not replace the detail of the row clicked last.
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import Applications from './Applications.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { mockApplicationDetail, mockApplicationAssets } from '../components/applications/fixtures.js'

vi.mock('../wailsjs/go/main/App', () => ({
  GetApplications: vi.fn(), GetApplication: vi.fn(), GetApplicationAssets: vi.fn(),
  AddApplication: vi.fn(), SetApplicationStatus: vi.fn(), OpenURL: vi.fn(), OpenPathWithOS: vi.fn(),
  GetProfileDocumentData: vi.fn(() => Promise.resolve('')), GetProfileDocumentText: vi.fn(() => Promise.resolve('')),
}))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))

afterEach(cleanup)

describe('Applications detail ordering', () => {
  it('keeps the detail of the row clicked last', async () => {
    WailsApp.GetApplications.mockResolvedValue([
      { id: 'a', title: 'Row A', company: 'CoA', kind: 'job', status: 'pending', tags: [] },
      { id: 'b', title: 'Row B', company: 'CoB', kind: 'job', status: 'pending', tags: [] },
    ])
    WailsApp.GetApplicationAssets.mockResolvedValue(mockApplicationAssets)
    let resolveA
    WailsApp.GetApplication.mockImplementation((id) => id === 'a'
      ? new Promise(r => { resolveA = r })
      : Promise.resolve({ ...mockApplicationDetail, id: 'b', title: 'Row B' }))
    render(<Applications />)
    await screen.findByText('Row A')
    fireEvent.click(screen.getByText('Row A'))
    fireEvent.click(screen.getByText('Row B'))
    await screen.findByRole('dialog', { name: /Application detail: Row B/i })
    await act(async () => { resolveA({ ...mockApplicationDetail, id: 'a', title: 'Row A' }) })
    await waitFor(() => expect(screen.getByRole('dialog', { name: /Application detail: Row B/i })).toBeInTheDocument())
  })
})
