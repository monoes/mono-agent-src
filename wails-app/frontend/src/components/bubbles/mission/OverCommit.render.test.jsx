// @vitest-environment jsdom
// The over-committed chip and the pursuit's waiting reason: shown from the CLI's fields, nothing when not over, text only.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import i18n from '../../../i18n.js'

const { sess, go } = vi.hoisted(() => ({ sess: { show: vi.fn(), pulse: vi.fn(), list: vi.fn(), board: vi.fn(), pursuitStatus: vi.fn() }, go: { PursuitStartPreview: vi.fn(), PursuitStart: vi.fn() } }))
vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn() }, notify: vi.fn() }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../../../wailsjs/go/main/App', () => go)
vi.mock('../../ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(true) }))
import MissionStrip from './MissionStrip.jsx'
import { PlanCard } from './MissionCards.jsx'
import { forgetConversationSessions } from './useMissionSession.js'

const session = { id: 7, title: 'Mission 7', goal: 'g', state: 'running', archived: false, rev: 1, budget_usd: 5, spent_usd: 1, counts: { done: 1, total: 2 }, conversation_id: 'c1' }
const over = (o = {}) => ({ over: true, session_id: 7, by_session_id: 3, budget_usd: 5, committed_usd: 6.5, over_by_usd: 1.5, spent_usd: 1, held_usd: 0.5, children_usd: 5, ...o })
const notOver = { over: false, session_id: 7, by_session_id: 0, budget_usd: 0, committed_usd: 0, over_by_usd: 0, spent_usd: 0, held_usd: 0, children_usd: 0 }
const read = oc => {
  sess.list.mockResolvedValue({ sessions: [session] })
  sess.show.mockResolvedValue(oc === undefined ? { session } : { session, over_committed: oc })
}
const strip = async oc => { read(oc); render(<MissionStrip conversationId="c1" />); await screen.findByTestId('mission-strip') }
beforeEach(async () => {
  vi.clearAllMocks(); forgetConversationSessions(); await i18n.changeLanguage('en')
  sess.pulse.mockResolvedValue({}); sess.board.mockResolvedValue({ tasks: {} })
  sess.pursuitStatus.mockResolvedValue({ progress: { waiting: 'over-committed' } })
})
afterEach(cleanup)

describe('over-committed on the mission strip', () => {
  it('shows the dollars and the session when the CLI says it is over', async () => {
    await strip(over())
    const chip = await screen.findByTestId('mission-over-committed')
    expect(chip).toHaveTextContent('Over-committed by $1.50: session #3')
    expect(chip).toHaveAttribute('aria-label', 'Over-committed by $1.50: session #3')
  })
  it.each([['over false', notOver], ['field absent', undefined], ['null', null], ['not an object', 'yes']])('shows nothing with %s', async (_n, oc) => {
    await strip(oc)
    await Promise.resolve()
    expect(screen.queryByTestId('mission-over-committed')).toBeNull()
    expect(screen.queryByTestId('pursuit-waiting-over-committed')).toBeNull()
    expect(sess.pursuitStatus).not.toHaveBeenCalled()
  })
  it('renders hostile and very long values as plain text', async () => {
    const evil = '<img src=x onerror=alert(1)>'.repeat(20)
    await strip(over({ over_by_usd: evil, by_session_id: '<b>x</b>' + 'y'.repeat(300) }))
    const chip = await screen.findByTestId('mission-over-committed')
    expect(chip.querySelector('img, b')).toBeNull()
    expect(chip.textContent.length).toBeLessThan(120)
    expect(chip.textContent).toContain('<img src=x')
  })
  it('shows the pursuit waiting reason only while over and only when the pursuit waits for it', async () => {
    await strip(over())
    expect(await screen.findByTestId('pursuit-waiting-over-committed')).toHaveTextContent('Raise the mission')
    expect(sess.pursuitStatus).toHaveBeenCalledWith(7)
  })
  it.each([[{ progress: { waiting: 'parked' } }], [{ error: 'no pursuit', code: 'not_found' }], [{}]])('has no waiting line when the pursuit status is %j', async (doc) => {
    sess.pursuitStatus.mockResolvedValue(doc)
    await strip(over())
    await screen.findByTestId('mission-over-committed')
    await waitFor(() => expect(sess.pursuitStatus).toHaveBeenCalled())
    expect(screen.queryByTestId('pursuit-waiting-over-committed')).toBeNull()
  })
  it('reads in Spanish', async () => {
    await i18n.changeLanguage('es')
    await strip(over())
    expect(await screen.findByTestId('mission-over-committed')).toHaveTextContent('Sobrecomprometida por $1.50: sesión #3')
    expect(await screen.findByTestId('pursuit-waiting-over-committed')).toHaveTextContent('Sube el presupuesto')
  })
})

describe('over-committed on the plan card', () => {
  const card = oc => { read(oc); render(<PlanCard item={{ payload: { sessionId: 7, round: 1 } }} conversationId="c1" />) }
  it('shows the chip when over, nothing otherwise', async () => {
    card(over())
    expect(await screen.findByTestId('mission-over-committed')).toHaveTextContent('$1.50')
    cleanup()
    card(notOver)
    await screen.findByTestId('mission-plan')
    expect(screen.queryByTestId('mission-over-committed')).toBeNull()
  })
})
