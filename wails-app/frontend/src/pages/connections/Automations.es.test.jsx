// @vitest-environment jsdom
// Browser automations (cards, dialogs and review) in Spanish.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

const api = vi.hoisted(() => ({
  doctorAutomations: vi.fn(),
  installAutomationDryRun: vi.fn(),
  chooseAutomationPackage: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn() }))

import i18n from '../../i18n.js'
import AutomationCard from './AutomationCard.jsx'
import HealthTab from './HealthTab.jsx'
import ImportDialog from './ImportDialog.jsx'

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

describe('browser automations in Spanish', () => {
  it('translates a card, with plural actions and the value chips', () => {
    render(<AutomationCard onOpen={() => {}} automation={{ id: 'a', name: 'Acme', version: '1.0.0', source: 'imported', actions: 1, session: { status: 'expired' }, modified: true }} />)
    expect(screen.getByText('1 acción')).toBeInTheDocument()
    expect(screen.getByText('sesión caducada')).toBeInTheDocument()
    expect(screen.getByText('importado 1.0.0')).toBeInTheDocument()
    expect(screen.getByText('modificada')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Acme, sesión caducada, 1 acción' })).toBeInTheDocument()
  })

  it('translates selector health', async () => {
    api.doctorAutomations.mockResolvedValue({ automations: [{ id: 'a', issues: [], selectors: [{ key: 'k', ok: 1, fail: 2, healed: 2, status: 'broken' }] }] })
    render(<HealthTab automationId="a" />)
    expect(await screen.findByText('roto')).toBeInTheDocument()
    expect(screen.getByText('1 selector en seguimiento; requieren atención: 1.')).toBeInTheDocument()
    expect(screen.getByText('2 reparados')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Volver a grabar' })).toBeInTheDocument()
  })

  it('translates the install review', async () => {
    api.chooseAutomationPackage.mockResolvedValue('/tmp/p.mpkg')
    api.installAutomationDryRun.mockResolvedValue({
      id: 'hn', version: '2.0.0', sha256: 'ab',
      review: { replaces: { id: 'hn', source: 'builtin' }, replaceRequired: true, actionEffects: { reply: 'message' } },
    })
    render(<ImportDialog onClose={() => {}} />)
    fireEvent.click(screen.getByText('Examinar'))
    expect(await screen.findByText('Reemplaza el paquete integrado hn')).toBeInTheDocument()
    expect(screen.getByText('Reemplazar hn (integrado)')).toBeDisabled()
    expect(screen.getByText('mensaje')).toBeInTheDocument()
    expect(screen.getByText('Dominios')).toBeInTheDocument()
  })
})
