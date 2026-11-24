// @vitest-environment jsdom
// W4 R21-13 / r21-D-04: sending a draft asks first; a failed Send shows its error with the compose panel closed.
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../i18n.js'

const confirmMock = vi.fn()
const sendDraft = vi.fn()
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: (...a) => confirmMock(...a), default: () => null }))
vi.mock('../components/MessageDetailModal.jsx', () => ({ default: () => null }))
vi.mock('../components/StatusHistoryModal.jsx', () => ({ default: () => null }))
vi.mock('../components/ProfileDetails.jsx', () => ({
  ExperienceSection: () => null, EducationSection: () => null, ProfileDetailsSection: () => null,
}))
vi.mock('../services/api.js', () => {
  const base = {
    getPersonDetail: () => Promise.resolve({ id: 'p1', username: 'sam', platform: 'email', full_name: 'Sam' }),
    getPersonInteractions: () => Promise.resolve([]),
    getPersonPosts: () => Promise.resolve([]),
    getPersonMessages: () => Promise.resolve([
      { id: 'd1', person_id: 'p1', direction: 'outbound', status: 'draft', source: 'outlook', subject: 'Hi', body: 'draft body' },
    ]),
    getLatestPersonStatus: () => Promise.resolve(null),
    markPersonMessagesRead: () => Promise.resolve(null),
    listConnections: () => Promise.resolve([]),
    sendDraftPersonMessage: (...a) => sendDraft(...a),
  }
  return {
    api: new Proxy(base, { get: (t, k) => (k in t ? t[k] : () => Promise.resolve([])) }),
    PLATFORM_COLORS: {}, STATE_COLORS: {},
  }
})
import Profile from './Profile.jsx'

afterEach(() => { cleanup(); confirmMock.mockReset(); sendDraft.mockReset() })

describe('Profile draft Send', () => {
  it('does not send when the confirmation is declined', async () => {
    confirmMock.mockResolvedValue(false)
    render(<Profile id="p1" onBack={() => {}} onOpenURL={() => {}} onOpenPost={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: /^Messages/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'Send' }))
    await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(1))
    expect(sendDraft).not.toHaveBeenCalled()
  })

  it('shows the error of a failed Send', async () => {
    confirmMock.mockResolvedValue(true)
    sendDraft.mockRejectedValue(new Error('smtp refused'))
    render(<Profile id="p1" onBack={() => {}} onOpenURL={() => {}} onOpenPost={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: /^Messages/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'Send' }))
    expect(await screen.findByTestId('draft-error')).toHaveTextContent('smtp refused')
  })
})
