// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../../i18n.js'
import { OutcomeBanner } from './orgsPanelParts.jsx'
import { TierChip, Badge, errorOf } from './ui.jsx'
import { SectionBudgetView } from './SectionBudgetPanel.jsx'
import { jevSummary } from './DecisionsFeed.jsx'
import { routeLabel } from './autonomyModel.js'
import { waitedLabel, idleStopLabel } from './waiting.js'
import { pendingRawItems } from './needsYouModel.js'

describe('orgs views in Spanish', () => {
  beforeAll(async () => { await i18n.changeLanguage('es') })
  afterAll(async () => { await i18n.changeLanguage('en') })
  afterEach(cleanup)

  it('words the outcome, tier and badge', () => {
    render(<><OutcomeBanner outcome={{ status: 'achieved', by: 'lead' }} /><TierChip tier="routine" /><Badge count={2} /></>)
    expect(screen.getByText('Logrado')).toBeInTheDocument()
    expect(screen.getByText('por lead')).toBeInTheDocument()
    expect(screen.getByText('rutinaria')).toHaveAttribute('title', 'Nivel de riesgo: rutinaria')
    expect(screen.getByLabelText('2 pendientes')).toBeInTheDocument()
  })

  it('words the section budgets', () => {
    const scope = { kind: 'section', name: 'core', allocation_usd: 2, spent_usd: 1, fraction: 0.5 }
    render(<SectionBudgetView report={{ sections: [scope], total: { ...scope, kind: 'total' }, total_tokens: 12, cost_complete: false }} />)
    expect(screen.getByText('Presupuestos por sección')).toBeInTheDocument()
    expect(screen.getByText('costo incompleto: cota inferior')).toBeInTheDocument()
  })

  it('words the plain helpers at call time', () => {
    expect(errorOf(null)).toBe('la solicitud falló')
    expect(jevSummary({ jev: { decided: false, p: 0.62, threshold: 0.8 } })).toBe('decidió el modelo (Jev p=0.62, menor que 0.8)')
    expect(routeLabel('decider', 'boss')).toBe('decide el jefe')
    expect(waitedLabel(1000000000000, 1000000045000)).toBe('esperando 45s')
    expect(idleStopLabel(0)).toBe('la organización puede detenerse por inactividad ya')
    expect(pendingRawItems(null, null, { approvals: [{ roleId: 'r', action: 'Bash', approved: null }] })[0].text).toBe('¿Aprobar Bash?')
  })
})
