// @vitest-environment jsdom
// W4 R21-26: a load failure is not an empty vault, and clears once a load works.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../i18n.js'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', () => ({
  ListSecrets: vi.fn(), AddSecret: vi.fn(), DeleteSecret: vi.fn(), ExportVaultAll: vi.fn(), ImportVaultAll: vi.fn(), OpenVaultImportFilePicker: vi.fn(),
}))
vi.mock('../components/VaultItemModal.jsx', () => ({ default: () => null }))
vi.mock('../components/VaultAgentAccess.jsx', () => ({ default: () => null }))
import Vault from './Vault.jsx'

afterEach(cleanup)

describe('Vault load', () => {
  it('shows the failure instead of "No vault entries", then clears it after a good reload', async () => {
    WailsApp.ListSecrets.mockRejectedValueOnce(new Error('keyring locked'))
    const { rerender } = render(<Vault isActive />)
    expect(await screen.findByText(/Failed to load vault: .*keyring locked/)).toBeInTheDocument()
    WailsApp.ListSecrets.mockResolvedValue([])
    rerender(<Vault isActive={false} />)
    rerender(<Vault isActive />)
    await waitFor(() => expect(screen.queryByText(/Failed to load vault/)).toBeNull())
    expect(screen.getByText(/No vault entries yet/)).toBeInTheDocument()
  })
})
