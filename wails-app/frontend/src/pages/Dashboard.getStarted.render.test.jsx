// @vitest-environment jsdom
// W5 r29-F3/F4/F5: the checklist shows with no profile, can be shown again, and the Help link reopens it.
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k) => k }) }))
vi.mock('../wailsjs/go/main/App', () => ({ GetVersion: () => Promise.resolve({ version: 'v1.2.3' }) }))
vi.mock('../services/api.js', () => ({ api: {}, PLATFORM_COLORS: {}, subscribeEvent: () => () => {} }))
vi.mock('../lib/health.js', () => ({
  getHealth: () => ({ report: { results: [] } }),
  subscribeHealth: () => () => {},
  summarize: () => ({ level: 'ok', issues: 0 }),
  runHealth: vi.fn(),
}))
vi.mock('../components/NewProfileModal.jsx', () => ({ default: () => null }))
vi.mock('../components/account/AccountBanners.jsx', () => ({ SignInDialog: () => null }))
vi.mock('../components/settings/PairBrowser.jsx', () => ({
  usePairExtension: () => ({ pair: vi.fn(), busy: false, result: null }),
  PairButton: () => null,
  PairResult: () => null,
}))
vi.mock('./dashboard/useDashboardData.js', () => ({
  useDashboardData: () => ({
    loading: false, noProfile: true, summary: null, summaryFailed: false, orgs: null,
    workflows: [], executions: [], refresh: vi.fn(), setExecutions: vi.fn(), reloadLists: vi.fn(),
  }),
}))
import Dashboard from './Dashboard.jsx'
import { runHelpAction } from './Help/helpActions.js'
import { DISMISS_KEY, loadDismissed } from './dashboard/getStarted.js'

beforeEach(() => localStorage.clear())
afterEach(cleanup)

const mount = () => render(<Dashboard onNavigate={vi.fn()} onOpenHil={vi.fn()} onRefresh={vi.fn()} />)

describe('Dashboard get-started checklist', () => {
  it('shows the steps even when no profile exists, with a labelled list', () => {
    mount()
    expect(screen.getByTestId('get-started')).toBeInTheDocument()
    expect(screen.getByTestId('dash-no-profile')).toBeInTheDocument()
    expect(screen.getByRole('list', { name: 'dashboard.getStarted.title' })).toBeInTheDocument()
  })

  it('can be shown again after it was hidden', () => {
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'dashboard.getStarted.dismiss' }))
    expect(screen.queryByTestId('get-started')).toBeNull()
    expect(loadDismissed()).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'getStartedReopen.showAgain' }))
    expect(screen.getByTestId('get-started')).toBeInTheDocument()
    expect(localStorage.getItem(DISMISS_KEY)).toBeNull()
  })

  it('the Help link to the dashboard checklist clears the dismissal', () => {
    localStorage.setItem(DISMISS_KEY, '1')
    const navigate = vi.fn()
    expect(runHelpAction({ kind: 'goto', target: 'dashboard', params: { showGetStarted: true } }, { navigate })).toEqual({ ok: true })
    expect(localStorage.getItem(DISMISS_KEY)).toBeNull()
    expect(navigate).toHaveBeenCalledWith('dashboard', { showGetStarted: true })
  })

  it('a plain dashboard link leaves a dismissal alone', () => {
    localStorage.setItem(DISMISS_KEY, '1')
    runHelpAction({ kind: 'goto', target: 'dashboard' }, { navigate: vi.fn() })
    expect(localStorage.getItem(DISMISS_KEY)).toBe('1')
  })
})
