// @vitest-environment jsdom
// The API side of the Connections page in Spanish.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

const api = vi.hoisted(() => ({
  showPlatform: vi.fn(() => Promise.resolve({})),
  getOAuthCredentials: vi.fn(() => Promise.resolve('')),
  openURL: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api,
  onConnectionProgress: () => () => {},
  onConnectionDone: () => () => {},
}))

import i18n from '../../i18n.js'
import ApiConnections from './ApiConnections.jsx'
import ApiConnectionModal from './ApiConnectionModal.jsx'
import { HELP_GUIDES } from '../../lib/helpGuides.js'

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

const platform = { id: 'telegram', name: 'Telegram', category: 'communication', methods: ['apikey'], fields: { apikey: [] } }

describe('API connections in Spanish', () => {
  it('translates the section, its categories and counts', () => {
    render(<ApiConnections platforms={[platform]} connections={[]} onSelect={() => {}} />)
    expect(screen.getByText('Conexiones de API')).toBeInTheDocument()
    expect(screen.getByText('Comunicación')).toBeInTheDocument()
    expect(screen.getByText('0 / 1 conectadas')).toBeInTheDocument()
    expect(screen.getByLabelText('Filtrar plataformas de API')).toBeInTheDocument()
  })

  it('translates the connection dialog and its help guide', () => {
    render(<ApiConnectionModal platform={platform} conn={null} onClose={() => {}} onRefresh={() => {}} onDisconnect={() => {}} />)
    expect(screen.getByText('Sin conectar')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Cómo obtener tus credenciales'))
    expect(screen.getByRole('dialog', { name: 'Crea un token de bot de Telegram' })).toBeInTheDocument()
    expect(screen.getByText('Envía el comando /newbot')).toBeInTheDocument()
  })

  it('has a text in both languages for every guide title and step', () => {
    const keys = Object.values(HELP_GUIDES).flatMap(m => Object.values(m))
      .flatMap(g => [g.title, ...g.steps.map(s => (typeof s === 'string' ? s : s.text))])
    for (const lng of ['en', 'es']) expect(keys.filter(k => !i18n.exists(k, { lng }))).toEqual([])
  })
})
