// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../i18n.js'
import Documents from './Documents.jsx'
import { sourceLabel } from '../lib/documentSort.js'
import { summaryStatusText } from '../components/CaptureViewerModal.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', () => ({
  ListProfileDocuments: vi.fn(),
  ListExcludedDocuments: vi.fn(() => Promise.resolve([])),
  IsMonomindInitialized: vi.fn(() => Promise.resolve(true)),
}))

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
afterEach(cleanup)

describe('Documents in Spanish', () => {
  it('shows the toolbar, columns and badges in Spanish', async () => {
    WailsApp.ListProfileDocuments.mockResolvedValue([
      { id: 'd1', filename: 'resume.pdf', source: 'upload', size_bytes: 100, created_at: '2026-01-01', indexed: false },
    ])
    render(<Documents />)
    expect(await screen.findByText('resume.pdf')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Documentos' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Indexar todo' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Subir carpeta' })).toBeInTheDocument()
    expect(screen.getByTitle('Ordenar por nombre')).toBeInTheDocument()
    expect(screen.getByText('Sin indexar')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Eliminar resume.pdf' })).toBeInTheDocument()
  })

  it('translates source labels and summary states when asked', () => {
    expect(sourceLabel('discovered')).toBe('Carpeta del perfil')
    expect(sourceLabel('my-own-source')).toBe('my-own-source')
    expect(summaryStatusText({ status: 'running', runtime: 'claude' })).toBe('claude está redactando el resumen…')
  })
})
