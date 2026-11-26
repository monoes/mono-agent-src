// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../../i18n.js'

vi.mock('../../services/api.js', () => ({ api: {}, notify: vi.fn(), onOrgEvent: vi.fn(() => () => {}) }))

import { validateStructure } from './orgGraph.js'
import { withSectionErrors } from './sectionsGraph.js'
import { mergeValidation } from './orgValidateReport.js'
import { budgetBadge, budgetLine } from './budgetModel.js'
import { statusMeta } from './orgDocuments.js'
import RoleNode from './RoleNode.jsx'
import { FullAccessBadge } from './fullAccess.jsx'
import { ValidationIssues } from './DesignerToolbar.jsx'

const role = (id, parentId) => ({ id, parentId, title: '', type: '', responsibilities: [], rest: {} })

describe('org designer in Spanish', () => {
  beforeAll(async () => { await i18n.changeLanguage('es') })
  afterAll(async () => { await i18n.changeLanguage('en') })
  afterEach(cleanup)

  it('words the role card, access badge and issue count', () => {
    render(<>
      <RoleNode node={role('a', null)} isRoot />
      <FullAccessBadge entry={{ access_state: 'active' }} />
      <ValidationIssues errors={['x', 'y']} />
    </>)
    expect(screen.getByText('Rol sin título')).toBeInTheDocument()
    expect(screen.getByText('RAÍZ')).toBeInTheDocument()
    expect(screen.getByTitle('Acceso completo')).toHaveTextContent('ACCESO COMPLETO')
    expect(screen.getByRole('button', { name: '2 problemas' })).toBeInTheDocument()
  })

  it('words the plain helpers at call time', () => {
    expect(budgetLine({ spent_usd: 1 })).toBe('$1.00 gastado')
    expect(budgetBadge({ soft_closed: true }).label).toBe('cierre suave')
    expect(statusMeta('accepted').label).toBe('aceptado')
  })

  it('shows structural errors in Spanish and still skips the CLI lines that repeat them', () => {
    const nodes = [role('a', null), role('b', 'b')]
    const sections = [{ name: 's', roster: ['x'], leadId: 'x', publishes: [], consumes: [] }]
    const structural = withSectionErrors(validateStructure(nodes), nodes, sections)
    expect(structural.errors[0]).toBe('el rol "b" reporta a sí mismo')
    const merged = mergeValidation(structural, { errors: [
      'role "b" reports to itself',
      "roles.b: a role outside every section can only be the root — add it to a section's members or make it a lead",
      'something only the CLI knows',
    ] })
    expect(merged.cliErrors).toEqual(['something only the CLI knows'])
  })
})
