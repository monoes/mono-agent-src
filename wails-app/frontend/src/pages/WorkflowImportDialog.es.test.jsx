// @vitest-environment jsdom
// The workflow import dialog in Spanish.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

const api = vi.hoisted(() => ({ importWorkflowFull: vi.fn(), chooseWorkflowFile: vi.fn() }))
vi.mock('../services/api.js', () => ({ api }))

import i18n from '../i18n.js'
import WorkflowImportDialog from './WorkflowImportDialog.jsx'

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

describe('WorkflowImportDialog in Spanish', () => {
  it('translates the dialog, the result and the bundled automations', async () => {
    api.importWorkflowFull.mockResolvedValue({
      id: 'w1', name: 'Resumen', status: 'created',
      automations: [{ id: 'books', version: '0.1.0', status: 'missing' }],
    })
    render(<WorkflowImportDialog onClose={() => {}} onOpen={() => {}} />)
    expect(screen.getByText('Importar flujo de trabajo')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Ruta del archivo del flujo de trabajo'), { target: { value: '/w/a.json' } })
    fireEvent.click(screen.getByText('Importar'))
    expect(await screen.findByText('Se importó “Resumen”.')).toBeInTheDocument()
    expect(screen.getByText('falta')).toBeInTheDocument()
    expect(screen.getByText(/necesita una automatización que no está instalada. Sus nodos de books/)).toBeInTheDocument()
    expect(screen.getByText('Instalar automatizaciones incluidas')).toBeInTheDocument()
  })
})
