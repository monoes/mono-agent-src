// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import * as WailsApp from '../wailsjs/go/main/App'
import MediaVault from './MediaVault.jsx'

// A file the extension saved (vault.save) shows as a file with its name, not
// as a broken <img>; audio and video show with their kind; images still get
// their thumbnail.
vi.mock('../wailsjs/go/main/App', () => ({
  GetVaultImages: vi.fn(() => Promise.resolve([
    { id: 'img-001', filename: 'img-001.png', label: 'Logo', source: 'upload', size_bytes: 10 },
    { id: 'img-002', filename: 'img-002.pdf', label: 'invoice.pdf', source: 'extension', size_bytes: 20 },
    { id: 'img-003', filename: 'img-003.mp4', kind: 'video', label: 'demo.mp4', source: 'upload', size_bytes: 30 },
    { id: 'img-004', filename: 'img-004.mp3', kind: 'audio', label: 'theme.mp3', source: 'upload', size_bytes: 40 },
  ])),
  GetVaultStats: vi.fn(() => Promise.resolve({ count: 4, total_bytes: 100 })),
  GetVaultImageData: vi.fn(() => Promise.resolve('data:image/png;base64,AAAA')),
}))
vi.mock('../services/api.js', () => ({ onImagesChanged: () => () => {} }))
vi.mock('../components/ImageDetailModal', () => ({ default: () => null }))

afterEach(cleanup)

describe('MediaVault', () => {
  it('shows a saved PDF as a file with its name and only fetches thumbnails for images', async () => {
    const { container } = render(<MediaVault />)
    const file = await screen.findByRole('img', { name: 'file invoice.pdf' })
    expect(file).toHaveTextContent('PDF')
    expect(file.querySelector('img')).toBeNull()
    await waitFor(() => expect(container.querySelectorAll('img')).toHaveLength(1))
    expect(WailsApp.GetVaultImageData).toHaveBeenCalledTimes(1)
    expect(WailsApp.GetVaultImageData).toHaveBeenCalledWith('img-001')
  })

  it('shows audio and video by kind, without fetching a thumbnail for them', async () => {
    render(<MediaVault />)
    const video = await screen.findByRole('img', { name: 'video demo.mp4' })
    expect(video).toHaveTextContent('MP4')
    const audio = await screen.findByRole('img', { name: 'audio theme.mp3' })
    expect(audio).toHaveTextContent('MP3')
    expect(await screen.findByText('Media Vault')).toBeInTheDocument()
    expect(WailsApp.GetVaultImageData).not.toHaveBeenCalledWith('img-003')
    expect(WailsApp.GetVaultImageData).not.toHaveBeenCalledWith('img-004')
  })
})
