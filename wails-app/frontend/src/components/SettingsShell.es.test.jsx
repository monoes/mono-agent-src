// @vitest-environment jsdom
import React from 'react'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import i18n from '../i18n.js'
import StatusBar from './StatusBar.jsx'
import UpdateNotice from './UpdateNotice.jsx'
import ConfirmHost, { confirm } from './ConfirmDialog.jsx'
import KeyValueFields, { newRow, validateRows } from './KeyValueFields.jsx'
import StatusHistoryModal from './StatusHistoryModal.jsx'
import VaultAgentAccess from './VaultAgentAccess.jsx'

vi.mock('../services/api.js', () => ({
  api: { getPersonStatusHistory: () => Promise.resolve([]) },
  subscribeEvent: () => () => {},
}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1.0.0' }),
  CheckForUpdate: () => Promise.resolve({}),
  AppSelfUpdate: () => Promise.resolve({}),
  QuitApp: () => {},
  RunHealthCheck: () => Promise.resolve('{}'),
  RunHealthFix: () => Promise.resolve('{}'),
  InstallAgentRuntime: () => Promise.resolve('{}'),
  CancelHealthRun: () => Promise.resolve(),
  GetVaultAgentAccess: () => Promise.reject('sin CLI'),
  SetVaultAgentAccess: () => Promise.resolve({}),
}))

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
afterEach(cleanup)

describe('app shell in Spanish', () => {
  it('status bar', () => {
    render(<StatusBar stats={{ executions_by_status: { RUNNING: 2 } }} dbConnected onToggleHil={() => {}} hilCount={2} />)
    expect(screen.getByText('Conectado')).toBeInTheDocument()
    expect(screen.getByText('2 en ejecución')).toBeInTheDocument()
    expect(screen.getByTitle('2 revisiones pendientes')).toHaveAccessibleName('Abrir Humano en el bucle')
  })

  it('update notice, field editor and status history', async () => {
    render(<>
      <UpdateNotice notice="n" />
      <KeyValueFields rows={[newRow()]} onChange={() => {}} />
      <StatusHistoryModal personId="p" onClose={() => {}} />
    </>)
    expect(screen.getByRole('button', { name: 'Reiniciar ahora' })).toBeInTheDocument()
    expect(screen.getByPlaceholderText('clave')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Añadir campo' })).toBeInTheDocument()
    expect(await screen.findByText('Aún no hay actualizaciones de estado')).toBeInTheDocument()
    expect(validateRows([newRow('', 'v')]).error).toBe('Todo campo con un valor necesita una clave.')
  })

  it('confirm dialog defaults', async () => {
    render(<ConfirmHost />)
    let answer
    act(() => { answer = confirm('?') })
    fireEvent.click(await screen.findByRole('button', { name: 'Cancelar' }))
    await expect(answer).resolves.toBe(false)
  })

  it('vault access, with a load error that follows the language', async () => {
    render(<VaultAgentAccess />)
    expect(await screen.findByRole('alert')).toHaveTextContent('No se pudo cargar el acceso de los agentes: sin CLI')
    expect(screen.getByRole('button', { name: 'Reintentar' })).toBeInTheDocument()
    await act(() => i18n.changeLanguage('en'))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not load agent access: sin CLI'))
    await act(() => i18n.changeLanguage('es'))
  })
})
