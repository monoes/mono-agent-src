// @vitest-environment jsdom
// The resource picker and the generate dialog in Spanish.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

const go = vi.hoisted(() => ({
  ListResources: vi.fn(() => Promise.resolve({ needs_reauth: true })),
  CreateResource: vi.fn(),
  ConnectPlatformOAuth: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => go)

import i18n from '../i18n.js'
import ResourcePickerField from './ResourcePickerField.jsx'
import GenerateWorkflowDialog from './GenerateWorkflowDialog.jsx'

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

describe('workflow editor pieces in Spanish', () => {
  it('translates the resource picker, keeping the field label it is given', async () => {
    render(<ResourcePickerField field={{ label: 'Hoja', resource: { type: 'spreadsheet' } }} value="" onChange={() => {}} platform="google_sheets" />)
    const search = screen.getByPlaceholderText('Buscar spreadsheet...')
    fireEvent.focus(search)
    expect(screen.getByText('Seleccionar Hoja')).toBeInTheDocument()
    expect(await screen.findByText('La autenticación ha caducado: vuelve a conectar tu cuenta.')).toBeInTheDocument()
    expect(screen.getByText('Volver a conectar la cuenta')).toBeInTheDocument()
  })

  it('translates the generate dialog', () => {
    render(<GenerateWorkflowDialog onClose={() => {}} />)
    expect(screen.getByText('Generar un flujo de trabajo con coder')).toBeInTheDocument()
    expect(screen.getByLabelText('Describe lo que debe hacer el flujo de trabajo.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Generar con coder' })).toBeDisabled()
  })
})
