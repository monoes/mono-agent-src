// @vitest-environment jsdom
import React from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'

const run = vi.hoisted(() => vi.fn(() => Promise.resolve({ error: 'stop here' })))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVaultImageData: () => Promise.resolve('data:image/png;base64,'),
  AddVaultImage: () => Promise.resolve({ id: 'img-002' }),
  RunNode: run,
}))
import ImageEditorFullscreen from './ImageEditorFullscreen.jsx'
import ImageEditorPanel from './ImageEditorPanel.jsx'

afterEach(() => { cleanup(); run.mockClear() })

const image = { id: 'img-001', label: 'photo', path: '/tmp/photo.png' }

describe('image editors', () => {
  it('the full-screen editor offers the four remaining tools and no background removal', () => {
    render(<ImageEditorFullscreen image={image} onClose={() => {}} />)
    for (const name of ['Adjust', 'Resize', 'Crop', 'Convert']) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument()
    }
    expect(screen.queryByRole('button', { name: /Remove BG/ })).toBeNull()
  })

  it('converting still runs image.convert', async () => {
    render(<ImageEditorFullscreen image={image} onClose={() => {}} initialTab="convert" />)
    fireEvent.click(screen.getByRole('button', { name: /Process/ }))
    await waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    expect(run.mock.calls[0][0].node_type).toBe('image.convert')
  })

  it('the side panel opens on resize and offers no background removal', async () => {
    render(<ImageEditorPanel image={image} onClose={() => {}} />)
    expect(screen.queryByRole('button', { name: /Remove BG/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /Apply/ }))
    await waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    expect(run.mock.calls[0][0].node_type).toBe('image.resize')
  })
})
