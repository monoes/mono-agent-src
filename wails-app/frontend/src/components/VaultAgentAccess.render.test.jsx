// @vitest-environment jsdom
import React from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import VaultAgentAccess from './VaultAgentAccess.jsx'
import { confirm } from './ConfirmDialog.jsx'

vi.mock('./ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))
const App = {}
beforeEach(() => {
  App.GetVaultAgentAccess = vi.fn().mockResolvedValue({ level: 'strict', profile_id: 'work' })
  App.SetVaultAgentAccess = vi.fn().mockImplementation(level => Promise.resolve({ level, profile_id: 'work' }))
  window.go = { main: { App } }
  confirm.mockReset().mockResolvedValue(true)
})
afterEach(() => { cleanup(); delete window.go })

describe('VaultAgentAccess', () => {
  it('loads the current profile policy and offers all three levels', async () => {
    render(<VaultAgentAccess />)
    const select = screen.getByRole('combobox', { name: /Agent access/ })
    expect(select).toBeDisabled()
    await waitFor(() => expect(select).toHaveValue('strict'))
    expect(select).toBeEnabled()
    expect(screen.getAllByRole('option')).toHaveLength(3)
    expect(screen.getByText(/Agents cannot read or use/)).toBeInTheDocument()
  })

  it('requires confirmation before revealing and saves only the chosen level', async () => {
    render(<VaultAgentAccess />)
    const select = screen.getByRole('combobox')
    await waitFor(() => expect(select).toBeEnabled())
    fireEvent.change(select, { target: { value: 'reveal' } })
    await waitFor(() => expect(App.SetVaultAgentAccess).toHaveBeenCalledWith('reveal'))
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('retrieve plaintext passwords'), expect.any(Object))
    await waitFor(() => expect(select).toHaveValue('reveal'))
    expect(screen.getByText(/Agents can retrieve plaintext/)).toBeInTheDocument()
  })

  it('keeps the saved level when access confirmation is cancelled', async () => {
    confirm.mockResolvedValue(false)
    render(<VaultAgentAccess />)
    const select = screen.getByRole('combobox')
    await waitFor(() => expect(select).toBeEnabled())
    fireEvent.change(select, { target: { value: 'use' } })
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    await waitFor(() => expect(select).toBeEnabled())
    expect(select).toHaveValue('strict')
    expect(App.SetVaultAgentAccess).not.toHaveBeenCalled()
  })

  it('disables changes during save and retains the prior level after a failure', async () => {
    let reject
    App.SetVaultAgentAccess.mockReturnValue(new Promise((_, r) => { reject = r }))
    render(<VaultAgentAccess />)
    const select = screen.getByRole('combobox')
    await waitFor(() => expect(select).toBeEnabled())
    fireEvent.change(select, { target: { value: 'use' } })
    await waitFor(() => expect(App.SetVaultAgentAccess).toHaveBeenCalledWith('use'))
    expect(select).toBeDisabled()
    reject(new Error('vault unavailable'))
    expect(await screen.findByRole('alert')).toHaveTextContent('vault unavailable')
    expect(select).toHaveValue('strict')
    expect(select).toBeEnabled()
  })

  it('revokes access without another enabling confirmation', async () => {
    App.GetVaultAgentAccess.mockResolvedValue({ level: 'reveal' })
    render(<VaultAgentAccess />)
    const select = screen.getByRole('combobox')
    await waitFor(() => expect(select).toHaveValue('reveal'))
    fireEvent.change(select, { target: { value: 'strict' } })
    await waitFor(() => expect(select).toHaveValue('strict'))
    expect(App.SetVaultAgentAccess).toHaveBeenCalledWith('strict')
    expect(confirm).not.toHaveBeenCalled()
  })

  it('reloads when the Vault page is activated and displays existing scope', async () => {
    const view = render(<VaultAgentAccess isActive={false} />)
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveValue('strict'))
    App.GetVaultAgentAccess.mockResolvedValue({ level: 'use', secrets: ['work-token'], hosts: ['example.com'] })
    view.rerender(<VaultAgentAccess isActive />)
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveValue('use'))
    expect(screen.getByText(/Current scope: work-token/)).toHaveTextContent('Hosts: example.com')
  })

  it('does not offer changes after loading fails, and allows retrying', async () => {
    App.GetVaultAgentAccess.mockRejectedValueOnce('CLI unavailable')
    render(<VaultAgentAccess />)
    expect(await screen.findByRole('alert')).toHaveTextContent('CLI unavailable')
    expect(screen.getByRole('combobox')).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveValue('strict'))
  })
})
