// @vitest-environment jsdom
// W4 R21-21..23, r21-D-08: load error clears on success, null-safe search, confirm before delete, drop hint.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../i18n.js'
import * as WailsApp from '../wailsjs/go/main/App'

const confirmMock = vi.fn()
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: (...a) => confirmMock(...a), default: () => null }))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVaultImages: vi.fn(), GetVaultStats: vi.fn(), DeleteVaultImage: vi.fn(() => Promise.resolve()),
  GetVaultImageData: vi.fn(() => Promise.resolve('')),
}))
vi.mock('../services/api.js', () => ({ onImagesChanged: () => () => {} }))
vi.mock('../components/ImageDetailModal', () => ({ default: () => null }))
import MediaVault from './MediaVault.jsx'

afterEach(() => { cleanup(); confirmMock.mockReset(); vi.clearAllMocks() })

const row = { id: 'img-1', filename: 'a.png', label: 'Logo', source: 'upload', size_bytes: 1 }

describe('MediaVault W4', () => {
  it('shows a load error, then clears it when Refresh works', async () => {
    WailsApp.GetVaultImages.mockRejectedValueOnce(new Error('boom'))
    WailsApp.GetVaultStats.mockResolvedValue({ count: 1, total_bytes: 1 })
    render(<MediaVault />)
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
    expect(screen.queryByText('loading…')).toBeNull()
    WailsApp.GetVaultImages.mockResolvedValue([row])
    fireEvent.click(screen.getByRole('button', { name: /refresh/i }))
    await screen.findByText('Logo')
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  })

  it('does not throw on a row without filename or source while searching', async () => {
    WailsApp.GetVaultImages.mockResolvedValue([row, { id: 'img-2', label: 'Bare' }])
    WailsApp.GetVaultStats.mockResolvedValue({ count: 2, total_bytes: 1 })
    render(<MediaVault />)
    await screen.findByText('Bare')
    fireEvent.change(screen.getByPlaceholderText('search…'), { target: { value: 'LOGO' } })
    expect(screen.getByText('Logo')).toBeInTheDocument()
    expect(screen.queryByText('Bare')).toBeNull()
  })

  it('asks before deleting, and deletes only when confirmed', async () => {
    WailsApp.GetVaultImages.mockResolvedValue([row])
    WailsApp.GetVaultStats.mockResolvedValue({ count: 1, total_bytes: 1 })
    confirmMock.mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    render(<MediaVault />)
    const del = await screen.findByRole('button', { name: 'Delete Logo' })
    fireEvent.click(del)
    await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(1))
    expect(WailsApp.DeleteVaultImage).not.toHaveBeenCalled()
    fireEvent.click(del)
    await waitFor(() => expect(WailsApp.DeleteVaultImage).toHaveBeenCalledWith('img-1'))
  })
})
