// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import i18n from '../i18n.js'

vi.mock('../services/api.js', () => ({
  api: {
    getAllPersonMessages: vi.fn(() => Promise.resolve([
      { id: 'm1', source: 'EMAIL', person_full_name: 'Ada', body: 'hola', direction: 'inbound', read_at: '2026-01-01' },
    ])),
    getPostDetail: vi.fn(() => Promise.resolve(null)),
    getPostComments: vi.fn(() => Promise.resolve([])),
  },
}))

import Communications from './Communications.jsx'
import PostDetail from './PostDetail.jsx'
import ImagePickerModal from '../components/ImagePickerModal.jsx'
import RefreshButton from '../components/RefreshButton.jsx'
import { vaultFileKind } from '../lib/vaultItems.js'

vi.mock('../wailsjs/go/main/App', () => ({ GetVaultImages: vi.fn(() => Promise.resolve([])) }))

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
afterEach(cleanup)

describe('messages, posts and images in Spanish', () => {
  it('shows the list and a message in Spanish', async () => {
    render(<Communications />)
    expect(await screen.findByText('Ada')).toBeInTheDocument()
    expect(screen.getByText('Comunicaciones')).toBeInTheDocument()
    expect(screen.getByText('1 mensaje entre todas las personas')).toBeInTheDocument()
    fireEvent.click(screen.getByText('(sin asunto)'))
    expect(screen.getByRole('dialog', { name: 'Detalle del mensaje' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Usar la selección' })).toBeInTheDocument()
  })

  it('says in Spanish that a post is missing', async () => {
    render(<PostDetail id="p1" onBack={() => {}} />)
    expect(await screen.findByText('Publicación no encontrada.')).toBeInTheDocument()
  })

  it('shows the picker, the refresh button and the file kind in Spanish', () => {
    render(<><ImagePickerModal onSelect={() => {}} onClose={() => {}} /><RefreshButton onClick={() => {}} /></>)
    expect(screen.getByRole('dialog', { name: 'Elige una imagen' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Actualizar' })).toBeInTheDocument()
    expect(vaultFileKind({ filename: 'noext' })).toBe('ARCHIVO')
  })
})
