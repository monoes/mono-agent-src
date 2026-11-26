// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../i18n.js'

vi.mock('../wailsjs/go/main/App', () => ({
  GetApplications: vi.fn(() => Promise.resolve([
    { id: 'a1', title: 'Engineer', company: 'Acme', kind: 'job', status: 'pending', tags: [] },
  ])),
}))

import Applications from './Applications.jsx'
import ApplicationDetailModal from '../components/applications/ApplicationDetailModal.jsx'

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
afterEach(cleanup)

describe('Applications in Spanish', () => {
  it('shows the list with Spanish headers and status words', async () => {
    render(<Applications />)
    expect(await screen.findByText('Engineer')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Candidaturas' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Procesar pendientes' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'pendiente de envío' })).toBeInTheDocument()
    expect(screen.getByText('empleo')).toBeInTheDocument()
  })

  it('shows the detail sections in Spanish', () => {
    render(<ApplicationDetailModal
      applicationId="a1"
      detail={{ id: 'a1', title: 'Engineer', kind: 'job', status: 'rejected', status_log: [{ to_status: 'rejected' }] }}
      initialAssets={{ documents: [], people: [], messages: [], tasks: [], task: null, fill: null, posting: null }}
      onClose={() => {}}
    />)
    expect(screen.getByText('Empresa desconocida')).toBeInTheDocument()
    expect(screen.getByRole('status', { name: 'Estado: rechazada' })).toBeInTheDocument()
    expect(screen.getByText('Historial de estados (1)')).toBeInTheDocument()
    expect(screen.getByText('(creada)')).toBeInTheDocument()
    expect(screen.getByText('Formulario rellenado')).toBeInTheDocument()
    expect(screen.getByText('No hay ninguna tarea de coder activa.')).toBeInTheDocument()
  })
})
