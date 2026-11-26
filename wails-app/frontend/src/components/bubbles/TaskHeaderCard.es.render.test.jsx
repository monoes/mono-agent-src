// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeAll, afterAll } from 'vitest'
import { render, screen, cleanup, renderHook, act } from '@testing-library/react'
import i18n from '../../i18n.js'
import { TaskHeaderCard } from './TaskHeaderCard.jsx'
import { usePersonItems } from './usePersonItems.js'

vi.mock('../SendToCoderApi.js', () => ({ aitaskApi: { get: vi.fn(async () => null) }, taskIsDone: s => ['complete', 'failed', 'cancelled'].includes(s) }))
vi.mock('../settings/aitaskBudgetApi.js', () => ({ aitaskBudgetApi: { forTask: vi.fn(async () => ({ capUsd: 2, spentUsd: 1, reported: true })) }, money: n => `$${n}` }))

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

describe('task header and person items in Spanish', () => {
  it('translates the status and the labels', async () => {
    render(<TaskHeaderCard task={{ taskId: 't1', goal: 'Fix it', status: 'needs_user', origin: { page: 'logs', label: 'Registros' } }} onNavigate={() => {}} />)
    expect(screen.getByText('necesita tu respuesta')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Abrir en Registros' })).toBeTruthy()
    expect(screen.getByRole('region', { name: 'Tarea de Coder' })).toBeTruthy()
    expect((await screen.findByTestId('task-budget')).getAttribute('title')).toBe('Gastado del límite de gasto de esta tarea')
  })

  it('shows a status it does not know as it came', () => {
    render(<TaskHeaderCard task={{ goal: 'x', status: 'parked' }} />)
    expect(screen.getByText('parked')).toBeTruthy()
  })

  it('says in Spanish why an item cannot be shown, keeping the code', async () => {
    const { result } = renderHook(() => usePersonItems({ enabled: false }))
    let res
    await act(async () => { res = await result.current.expand({ id: 'nope' }) })
    expect(res).toEqual({ error: 'elemento desconocido' })
  })
})
