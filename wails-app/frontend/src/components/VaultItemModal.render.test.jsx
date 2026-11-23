// @vitest-environment jsdom
import React from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import i18n from '../i18n.js'
import es from '../locales/es.json'
import VaultItemModal from './VaultItemModal.jsx'

vi.mock('../wailsjs/go/main/App', () => ({
  GetSecretFields: vi.fn().mockResolvedValue({ notes: '', fields: { token: 'x' } }),
  UpdateSecret: vi.fn(),
}))

beforeEach(() => i18n.changeLanguage('en'))
afterEach(() => { cleanup(); i18n.changeLanguage('en') })

describe('VaultItemModal', () => {
  it('is a labelled modal dialog that Escape closes and that keeps Tab inside', async () => {
    const onClose = vi.fn()
    render(<><button>opener</button><VaultItemModal entry={{ name: 'n', kind: 'login' }} onClose={onClose} onSaved={() => {}} /></>)
    const dlg = screen.getByRole('dialog')
    expect(dlg).toHaveAttribute('aria-modal', 'true')
    expect(dlg).toHaveAccessibleName(/Edit Login/)
    await waitFor(() => expect(screen.queryByText(/Loading fields/)).toBeNull())
    expect(dlg.contains(document.activeElement)).toBe(true)
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })

  it('speaks the chosen language', async () => {
    await i18n.changeLanguage('es')
    render(<VaultItemModal entry={{ name: 'n', kind: 'login' }} onClose={() => {}} onSaved={() => {}} />)
    expect(screen.getByRole('button', { name: es.vaultItem.save })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
  })
})
