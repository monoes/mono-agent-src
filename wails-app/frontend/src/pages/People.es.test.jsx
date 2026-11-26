// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import i18n from '../i18n.js'

vi.mock('../services/api.js', () => ({
  api: {
    getPeople: vi.fn(() => Promise.resolve([{ id: 'p1', username: 'ada', platform: 'LINKEDIN', created_at: '2026-01-01' }])),
    getPeopleCount: vi.fn(() => Promise.resolve(1)),
    getAllPersonMessages: vi.fn(() => Promise.resolve([])),
    getPeopleTagsMap: vi.fn(() => Promise.resolve({})),
    getPersonTags: vi.fn(() => Promise.resolve([])),
    getAllTags: vi.fn(() => Promise.resolve([])),
  },
  subscribeEvent: () => () => {},
}))

import People, { TAG_SUGGESTION_GROUPS } from './People.jsx'

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
afterEach(cleanup)

describe('People in Spanish', () => {
  it('shows the table and the tag editor in Spanish, keeping stored tag names', async () => {
    render(<People />)
    expect(await screen.findByText('@ada')).toBeInTheDocument()
    expect(screen.getByText('Personas')).toBeInTheDocument()
    expect(screen.getByText('1 perfil')).toBeInTheDocument()
    expect(screen.getByText('Nombre completo')).toBeInTheDocument()
    fireEvent.click(screen.getByTitle('Añadir o gestionar etiquetas'))
    expect(await screen.findByRole('dialog', { name: 'Gestionar las etiquetas de @ada' })).toBeInTheDocument()
    expect(screen.getByText('Embudo de ventas')).toBeInTheDocument()
    expect(screen.getByTitle('Haz clic para asignar "Lead"')).toBeInTheDocument()
  })

  it('reads the group titles in the current language', () => {
    expect(TAG_SUGGESTION_GROUPS.map(g => g.title)).toEqual(['Embudo de ventas', 'Cargo y autoridad', 'Apoyo y relaciones'])
  })
})
