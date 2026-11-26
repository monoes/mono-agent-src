// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeAll, afterAll } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../../i18n.js'
import { HILCard, DraftMessageCard, SuggestionChip } from './HILCards.jsx'
import { LeadApprovalCard } from './LeadApprovalCard.jsx'
import { OrgDecisionCard } from './OrgDecisionCard.jsx'

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

const noop = vi.fn()

describe('Human in Loop cards in Spanish', () => {
  it('translates a workflow pause and its suggestion, keeping the values', () => {
    render(<HILCard item={{ id: 'h1', node_name: 'Review', execution_id: 'abcdef123456', readonly_data: { a: 'x' }, node_config: { suggestion: { choice: 'needs_human', p: 0.91, risk: 'high' } } }} onApprove={noop} onReject={noop} />)
    const chip = screen.getByTestId('jev-suggestion')
    expect(chip.textContent).toBe('Sugerencia: requiere una persona 91% · riesgo alto')
    expect(chip.getAttribute('title')).toBe('Jev sugiere: requiere una persona (91% de confianza) · riesgo alto. Es solo una sugerencia; tú decides.')
    expect(screen.getByText('Aprobar y continuar')).toBeTruthy()
    expect(screen.getByText('Información de contexto')).toBeTruthy()
  })

  it('shows a choice it does not know as it came', () => {
    render(<SuggestionChip choice="hold_back" />)
    expect(screen.getByTestId('jev-suggestion').textContent).toBe('Sugerencia: hold back')
  })

  it('translates a draft and a lead', () => {
    render(<>
      <DraftMessageCard item={{ id: 'd1', person_full_name: 'Ada', body: 'Hi' }} onSend={noop} onReject={noop} />
      <LeadApprovalCard item={{ id: 'l1', full_name: 'Bo', suggestion: { suggest: 'reject', intro_fit: 'on_topic' } }} onApprove={noop} onReject={noop} />
    </>)
    expect(screen.getByText('(sin asunto)', { exact: false })).toBeTruthy()
    expect(screen.getByText('— para Ada')).toBeTruthy()
    expect(screen.getByText('Aprobar y enviar')).toBeTruthy()
    expect(screen.getByTestId('jev-suggestion').textContent).toBe('Sugerencia: rechazar · presentación pertinente')
  })

  it('translates an org gate and an approval with no request id, leaving the command', () => {
    render(<>
      <OrgDecisionCard item={{ id: 'g1', orgName: 'acme', kind: 'gate', role: 'dev', since: '5m' }} />
      <OrgDecisionCard item={{ id: 'a1', orgName: 'acme', kind: 'approval', role: 'dev', action: 'Bash' }} />
    </>)
    expect(screen.getByText('compuerta para dev')).toBeTruthy()
    expect(screen.getByText('Aprobar compuerta')).toBeTruthy()
    const note = screen.getByRole('note')
    expect(note.textContent).toBe('Esta solicitud no tiene ID de solicitud, así que la aplicación no puede resolverla por sí sola. Actualiza monomind o ejecuta monoagentcli --profile <profile> org approve|deny acme dev Bash en una terminal; eso resuelve todas las llamadas a Bash de dev que estén en espera.')
  })
})
