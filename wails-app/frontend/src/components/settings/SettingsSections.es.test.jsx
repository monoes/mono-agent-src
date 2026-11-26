// @vitest-environment jsdom
import React from 'react'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen } from '@testing-library/react'
import i18n from '../../i18n.js'
import ExtensionSection from './ExtensionSection.jsx'
import BrowserPermissionsSection from './BrowserPermissionsSection.jsx'
import BrowserBindingsSection from './BrowserBindingsSection.jsx'
import VaultKeyringSection from './VaultKeyringSection.jsx'
import AiTaskBudgetsSection from './AiTaskBudgetsSection.jsx'
import CoderModeSection from './CoderModeSection.jsx'
import JevSection from './JevSection.jsx'
import { BrowserSubmitDetails } from '../hil/BrowserSubmitDetails.jsx'
import { BrowserSiteDetails } from '../hil/BrowserSiteDetails.jsx'

const App = {}
beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
beforeEach(() => { window.go = { main: { App } } })
afterEach(() => { cleanup(); delete window.go })

const answer = (value) => () => Promise.resolve(value)

describe('settings sections in Spanish', () => {
  it('extension and browser bindings', async () => {
    App.GetExtensionInfo = answer({ path: '/x', installed: false, current: false, version: '' })
    App.GetBrowsers = answer({ running: true, browsers: [{ instance: '3f2a91c0-0000', label: 'Edge', profile_ids: ['a', 'b'], version: '1' }] })
    App.GetProfiles = answer([{ id: 'a', name: 'Trabajo', is_active: true }])
    App.GetExtensionStatus = answer('{}')
    render(<><ExtensionSection /><BrowserBindingsSection /></>)
    expect(await screen.findByRole('button', { name: 'Instalar' })).toBeInTheDocument()
    expect(screen.getByText('Cargar descomprimida')).toBeInTheDocument()
    expect(await screen.findByText('Trabajo (activo)')).toBeInTheDocument()
    expect(screen.getByText(/2 perfiles usan este navegador/)).toBeInTheDocument()
  })

  it('permissions, vault keyring and AI task budgets', async () => {
    App.BrowserPermissions = answer(JSON.stringify({ catalog: [], kinds: [], sites: [] }))
    App.KeyringStatus = answer({ backend: 'file', passphrase_file: {}, file_keyrings: [] })
    App.AITaskBudgets = answer(JSON.stringify({ operatorUsd: 5, caps: [{ scope: 'role', key: 'writer', capUsd: 0, usedUsd: 0 }] }))
    render(<><BrowserPermissionsSection /><VaultKeyringSection /><AiTaskBudgetsSection /></>)
    expect(await screen.findByText('Aún no hay reglas de sitios.')).toBeInTheDocument()
    expect(await screen.findByTestId('vault-keyring-chip')).toHaveTextContent('Frase de contraseña sin guardar')
    expect(await screen.findByText('Rol writer')).toBeInTheDocument()
    expect(screen.getByText('sin gasto')).toBeInTheDocument()
    expect(screen.getByTestId('aitask-budgets')).toHaveTextContent('el presupuesto de Coder ($5.00 por mensaje)')
  })

  it('coder mode and Jev', async () => {
    App.CoderStatus = answer(JSON.stringify({ enabled: false, workspaceRoot: '/w', ready: true, missingCapabilities: [], monomindVersion: '1', runtime: 'claude' }))
    App.JevStatus = answer({ profile_id: 'default', key_source: 'none', surfaces: [] })
    App.JevUsage = answer({ surfaces: [], total: { calls: 0 } })
    App.JevProviders = answer({ default: 'typesafe', providers: [] })
    render(<><CoderModeSection /><JevSection /></>)
    expect(await screen.findByLabelText('Carpeta raíz de Coder')).toHaveValue('/w')
    expect(await screen.findByTestId('jev-key-chip')).toHaveTextContent('Sin definir')
    expect(screen.getByText('No hay llamadas a Jev en los últimos 7 días.')).toBeInTheDocument()
  })

  it('browser approval details', () => {
    render(<>
      <BrowserSubmitDetails item={{ readonly_data: { requested_by: { role: 'qa' } } }} />
      <BrowserSiteDetails item={{ readonly_data: { kind: 'org', origin: 'https://x.com' } }} />
    </>)
    expect(screen.getByText('un agente (afirma ser: rol "qa")')).toBeInTheDocument()
    expect(screen.getByText('No hay campos con nombre')).toBeInTheDocument()
    expect(screen.getByTestId('browser-site-details')).toHaveTextContent('Un rol de organización quiere usar https://x.com')
  })
})
