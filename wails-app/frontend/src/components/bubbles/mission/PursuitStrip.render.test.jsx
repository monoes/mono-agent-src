// @vitest-environment jsdom
// The mission strip offers the pursuit start form on an open session, and only there.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import '../../../i18n.js'

const { sess, go } = vi.hoisted(() => ({ sess: { show: vi.fn(), pulse: vi.fn(), list: vi.fn(), board: vi.fn() }, go: { PursuitStartPreview: vi.fn(), PursuitStart: vi.fn() } }))
vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn() }, notify: vi.fn() }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../../../wailsjs/go/main/App', () => go)
vi.mock('../../ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(true) }))
import MissionStrip from './MissionStrip.jsx'
import { forgetConversationSessions } from './useMissionSession.js'

const doc = state => ({ id: 7, title: 'Mission 7', goal: 'g', state, archived: false, rev: 1, budget_usd: 5, spent_usd: 1, counts: { done: 1, total: 2 }, conversation_id: 'c1' })
const strip = async state => {
  sess.list.mockResolvedValue({ sessions: [doc(state)] })
  sess.show.mockResolvedValue({ session: doc(state) })
  render(<MissionStrip conversationId="c1" />)
  await screen.findByTestId('mission-strip')
}
beforeEach(() => { vi.clearAllMocks(); forgetConversationSessions(); sess.pulse.mockResolvedValue({}); sess.board.mockResolvedValue({ tasks: {} }) })
afterEach(cleanup)

describe('pursuit start in the mission strip', () => {
  it.each(['running', 'paused'])('a %s mission has a Start pursuit button that opens the form and reads nothing yet', async (state) => {
    await strip(state)
    expect(screen.queryByTestId('pursuit-form')).toBeNull()
    fireEvent.click(screen.getByTestId('mission-pursuit'))
    expect(screen.getByTestId('pursuit-form')).toBeInTheDocument()
    expect(screen.getByTestId('mission-pursuit')).toHaveAttribute('aria-expanded', 'true')
    expect(go.PursuitStartPreview).not.toHaveBeenCalled()
    expect(go.PursuitStart).not.toHaveBeenCalled()
  })
  it('a draft mission has none', async () => {
    await strip('draft')
    expect(screen.queryByTestId('mission-pursuit')).toBeNull()
  })
})
