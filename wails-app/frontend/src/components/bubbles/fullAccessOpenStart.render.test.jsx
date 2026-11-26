// @vitest-environment jsdom
// The open-ended start screen replaces the new chat's setup form, and Start
// there makes a chat: at full access it carries the same warning.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { api } = vi.hoisted(() => ({
  api: {
    getChatMissionToggle: vi.fn(() => Promise.resolve({})),
    getChatModeOptions: vi.fn(() => Promise.resolve({ options: [{ mode: 'off', available: true }, { mode: 'open', available: true }] })),
    getChatTurns: vi.fn(() => Promise.resolve({ items: [] })),
    getChatEvents: vi.fn(() => Promise.resolve({ items: [], hasMore: false })),
    coderStatus: vi.fn(() => Promise.resolve({ enabled: true, ready: true, defaultAccess: 'full', workspaceRoot: '/w' })),
    coderWorkspaceList: vi.fn(() => Promise.resolve([])),
    getAgentRuntimeModels: vi.fn(() => Promise.resolve([{ id: 'opus', label: 'Opus' }])),
    getProfiles: vi.fn(() => Promise.resolve([])),
  },
}))
vi.mock('../../services/api.js', () => ({
  api,
  notify: vi.fn(),
  onChatEvent: () => () => {},
  onOrgEvent: () => () => {},
  onOrgRunStatus: () => () => {},
  newOrgEventsStreamId: () => 'stream',
}))
vi.mock('../../lib/openStartApi.js', () => ({
  openStartApi: { entry: () => Promise.resolve({ mode_available: true, platform: 'linux', not_defended: '' }), render: vi.fn(), start: vi.fn() },
}))
vi.mock('../../services/controlRoom.js', async importOriginal => {
  const orig = await importOriginal()
  return { ...orig, controlRoomApi: { ...orig.controlRoomApi, status: () => Promise.resolve({ error: 'unread' }) } }
})

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

function Harness() {
  const store = useCoderBubbles()
  return <CoderBubbles store={store} onNavigate={() => {}} />
}

beforeEach(() => {
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
})
afterEach(cleanup)

describe('full access warning on the open-ended start screen', () => {
  it('is shown there once', async () => {
    render(<Harness />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    fireEvent.click(await screen.findByRole('radio', { name: 'Open-ended' }))
    await screen.findByTestId('open-start')
    expect(screen.getAllByTestId('full-access-warning')).toHaveLength(1)
    expect(screen.getByTestId('full-access-warning')).toHaveAttribute('role', 'alert')
  })
})
