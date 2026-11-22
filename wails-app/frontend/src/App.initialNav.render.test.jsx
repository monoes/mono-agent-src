// @vitest-environment jsdom
// App opens at the page the onboarding's last step asked for (initialNav), and
// the first health report does not take the person to Settings instead. Every
// page is a stub: this is about App's navigation only.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act } from '@testing-library/react'

const health = vi.hoisted(() => ({ listeners: [] }))
vi.mock('./lib/health.js', () => ({
  startBackgroundHealth: () => () => {},
  subscribeHealth: (fn) => { health.listeners.push(fn); return () => {} },
  summarize: (r) => ({ level: r?.level || 'ok' }),
  isFixing: () => false,
}))
const stub = vi.hoisted(() => (name) => async () => { const React = await import("react"); return { default: () => React.createElement("div", { "data-testid": `page-${name}` }) } })
vi.mock('./components/Sidebar.jsx', () => ({ default: ({ activePage }) => <nav data-testid="sidebar" data-active={activePage} /> }))
vi.mock('./lib/usePendingCount.js', () => ({ usePendingCount: () => ({ count: 0 }) }))
vi.mock('./components/StatusBar.jsx', () => ({ default: () => null }))
vi.mock('./components/Toasts.jsx', () => ({ default: () => null }))
vi.mock('./components/StartupOverlay.jsx', () => ({ default: () => null }))
vi.mock('./components/ConfirmDialog.jsx', () => ({ default: () => null }))
vi.mock('./components/bubbles/CoderBubbles.jsx', () => ({ default: () => null }))
vi.mock('./components/bubbles/PersonItemsContext.jsx', () => ({
  PersonItemsProvider: ({ children }) => children, usePersonItemsValue: () => ({ items: [] }),
}))
vi.mock('./components/bubbles/useCoderBubbles.js', () => ({ useCoderBubbles: () => ({ openConversation: () => {}, openDraft: () => {}, openNewChat: () => {} }) }))
vi.mock('./components/history/HistoryPanel.jsx', () => ({ default: () => null }))
vi.mock('./components/bubbles/useOrgBubbleWatch.js', () => ({ useOrgBubbleWatch: () => () => {} }))
vi.mock('./pages/HumanInLoop.jsx', () => ({ default: () => null }))
vi.mock('./pages/Dashboard.jsx', stub('dashboard'))
vi.mock('./pages/People.jsx', stub('people'))
vi.mock('./pages/Profile.jsx', stub('profile'))
vi.mock('./pages/PostDetail.jsx', stub('postDetail'))
vi.mock('./pages/Connections.jsx', stub('connections'))
vi.mock('./pages/Communications.jsx', stub('communications'))
vi.mock('./pages/Publication.jsx', stub('publication'))
vi.mock('./pages/Agents.jsx', stub('ai'))
vi.mock('./pages/Orgs.jsx', stub('orgs'))
vi.mock('./pages/Logs.jsx', stub('logs'))
vi.mock('./pages/NodeRunner.jsx', stub('noderunner'))
vi.mock('./pages/Settings.jsx', stub('settings'))
vi.mock('./pages/MediaVault.jsx', stub('vault'))
vi.mock('./pages/Vault.jsx', stub('secretsVault'))
vi.mock('./pages/Applications.jsx', stub('applications'))
vi.mock('./pages/Documents.jsx', stub('documents'))
vi.mock('./pages/Info.jsx', stub('info'))
vi.mock('./pages/Tasks.jsx', stub('tasks'))
vi.mock('./pages/ControlRoom/ControlRoom.jsx', stub('controlroom'))
vi.mock('./lib/useControlRoomProbe.js', () => ({ useControlRoomProbe: () => ({ show: true }) }))
vi.mock('./components/FileViewerModal.jsx', () => ({ default: () => null, fileViewerKind: () => null }))
vi.mock('./wailsjs/go/main/App', () => ({}))
vi.mock('./services/api.js', () => ({
  api: {
    isDBConnected: () => Promise.resolve(true), getDashboardStats: () => Promise.resolve(null),
    getLogs: () => Promise.resolve([]), listOrgDesigns: () => Promise.resolve([]), clearLogs: () => {},
  },
  notify: () => {}, onLogEntry: () => () => {}, onOrgDesignUpdated: () => () => {}, subscribeEvent: () => () => {},
}))

import App, { PAGE_IDS } from './App.jsx'

const active = () => screen.getByTestId('sidebar').getAttribute('data-active')
const report = async (level) => { await act(async () => { health.listeners.forEach(fn => fn({ report: { level } })) }) }

beforeEach(() => { health.listeners = [] })
afterEach(cleanup)

describe('App initialNav', () => {
  it('opens at the page the onboarding asked for', () => {
    render(<App initialNav={{ page: 'orgs' }} />)
    expect(active()).toBe('orgs')
    expect(screen.getByTestId('page-orgs')).toBeVisible()
  })

  it('keeps that page when the first health report is broken', async () => {
    render(<App initialNav={{ page: 'tasks' }} />)
    await report('broken')
    expect(active()).toBe('tasks')
  })

  it('still opens Settings for a broken report without an initial page', async () => {
    render(<App />)
    expect(active()).toBe('dashboard')
    await report('broken')
    expect(active()).toBe('settings')
  })

  it('navRequest navigates the mounted app, once per request, without remounting it', () => {
    const { rerender } = render(<App />)
    const sidebar = screen.getByTestId('sidebar')
    expect(active()).toBe('dashboard')
    const req = { page: 'orgs' }
    rerender(<App navRequest={req} />)
    expect(active()).toBe('orgs')
    expect(screen.getByTestId('sidebar')).toBe(sidebar)
    rerender(<App navRequest={req} />)
    rerender(<App navRequest={{ page: 'tasks' }} />)
    expect(active()).toBe('tasks')
  })

  it('PAGE_IDS are the pages the app can show', () => {
    for (const id of PAGE_IDS) {
      const { unmount } = render(<App initialNav={{ page: id }} />)
      expect(screen.getByTestId(`page-${id}`)).toBeVisible()
      unmount()
    }
  })
})
