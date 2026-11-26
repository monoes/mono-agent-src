// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const App = {}
beforeEach(() => {
  for (const k of ['KeyringStatus', 'KeyringSetPassphrase', 'KeyringClearPassphrase']) App[k] = vi.fn()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const PATH = '/home/u/.monoagent/keyring-passphrase'
const status = (backend, pf = {}, file_keyrings = []) => ({
  backend, os_keyring_error: backend === 'os' ? '' : 'no Secret Service', file_keyring_allowed: backend === 'file',
  file_keyrings,
  passphrase_file: { source: 'none', configured: false, path: PATH, exists: false, mode: '', ok: false, error: '', ...pf },
})
const configured = { source: 'configured', configured: true, exists: true, mode: '0600', ok: true }

async function mount(st) {
  App.KeyringStatus.mockResolvedValue(st)
  const { default: VaultKeyringSection } = await import('./VaultKeyringSection.jsx')
  return render(<VaultKeyringSection />)
}

describe('VaultKeyringSection', () => {
  it('renders nothing on a host with an OS keychain', async () => {
    const { container } = await mount(status('os'))
    await waitFor(() => expect(App.KeyringStatus).toHaveBeenCalled())
    await new Promise(r => setTimeout(r, 0))
    expect(container).toBeEmptyDOMElement()
    expect(screen.queryByTestId('vault-keyring-section')).not.toBeInTheDocument()
  })

  it('renders nothing when the file keyring is not allowed, or the status call fails', async () => {
    const { container } = await mount(status('unavailable'))
    await waitFor(() => expect(App.KeyringStatus).toHaveBeenCalled())
    await new Promise(r => setTimeout(r, 0))
    expect(container).toBeEmptyDOMElement()
    cleanup()
    App.KeyringStatus.mockRejectedValue(new Error('unknown command "keyring"'))
    const { default: VaultKeyringSection } = await import('./VaultKeyringSection.jsx')
    const r = render(<VaultKeyringSection />)
    await new Promise(res => setTimeout(res, 0))
    expect(r.container).toBeEmptyDOMElement()
  })

  it('on the file backend without a saved passphrase: explains and offers a password field', async () => {
    await mount(status('file'))
    expect(await screen.findByTestId('vault-keyring-chip')).toHaveTextContent('Passphrase not saved')
    expect(screen.getByText(/no system keychain/)).toBeInTheDocument()
    const input = screen.getByLabelText('Vault keyring passphrase')
    expect(input).toHaveAttribute('type', 'password')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Forget' })).not.toBeInTheDocument()
    expect(screen.getByText(/No vault keyring yet/)).toBeInTheDocument()
  })

  it('saves the passphrase through the CLI, clears the field and shows the configured state', async () => {
    await mount(status('file', {}, ['default']))
    App.KeyringSetPassphrase.mockResolvedValue({ path: PATH, saved: true })
    const input = await screen.findByLabelText('Vault keyring passphrase')
    fireEvent.change(input, { target: { value: 'hunter2 hunter2' } })
    App.KeyringStatus.mockResolvedValue(status('file', configured, ['default']))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(App.KeyringSetPassphrase).toHaveBeenCalledWith('hunter2 hunter2'))
    await screen.findByText(`Saved to ${PATH} (readable only by you).`)
    expect(input).toHaveValue('')
    await waitFor(() => expect(screen.getByTestId('vault-keyring-chip')).toHaveTextContent('Passphrase saved'))
    expect(screen.getByText(`${PATH} · mode 0600`)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Forget' })).toBeInTheDocument()
  })

  it('shows the CLI error when the passphrase does not unlock the existing keyring', async () => {
    await mount(status('file', {}, ['default']))
    App.KeyringSetPassphrase.mockRejectedValue('secrets: this passphrase does not unlock the existing file keyring of profile "default"')
    fireEvent.change(await screen.findByLabelText('Vault keyring passphrase'), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('does not unlock')
    expect(screen.getByTestId('vault-keyring-chip')).toHaveTextContent('Passphrase not saved')
  })

  it('forgets the saved passphrase', async () => {
    await mount(status('file', configured, ['default']))
    App.KeyringClearPassphrase.mockResolvedValue({ path: PATH, removed: true })
    App.KeyringStatus.mockResolvedValue(status('file', {}, ['default']))
    fireEvent.click(await screen.findByRole('button', { name: 'Forget' }))
    await screen.findByText(/Saved passphrase removed/)
    await waitFor(() => expect(screen.getByTestId('vault-keyring-chip')).toHaveTextContent('Passphrase not saved'))
  })

  it('reports a passphrase file with bad permissions', async () => {
    await mount(status('file', { ...configured, mode: '0644', ok: false, error: 'readable by other users (mode 0644); run chmod 600 on it' }))
    expect(await screen.findByTestId('vault-keyring-chip')).toHaveTextContent('Passphrase file has a problem')
    expect(screen.getByText(/run chmod 600/)).toBeInTheDocument()
  })
})
