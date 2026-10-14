// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k) => k }) }))
vi.mock('../../components/account/AccountBanners.jsx', () => ({ SignInDialog: () => <div role="dialog">sign in</div> }))
import GetStartedCard from './GetStartedCard.jsx'
import { getStartedSteps } from './getStarted.js'
import en from '../../locales/en.json'
import es from '../../locales/es.json'

afterEach(() => { cleanup(); delete window.go })

describe('GetStartedCard', () => {
  it('shows each step with its state and one action', () => {
    const onNavigate = vi.fn()
    const steps = getStartedSteps({ signedIn: true, okRuns: 0, bridge: 'unpaired', daemonRunning: false })
    render(<GetStartedCard steps={steps} bridge="unpaired" onNavigate={onNavigate} onDismiss={vi.fn()} />)
    expect(screen.getAllByText('dashboard.getStarted.done')).toHaveLength(1) // signed in
    expect(screen.queryByRole('button', { name: 'dashboard.getStarted.signInAction' })).not.toBeInTheDocument()
    expect(screen.getByText('dashboard.getStarted.extensionWhyUnpaired')).toBeInTheDocument()
    expect(screen.getByText('dashboard.getStarted.optional')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'dashboard.getStarted.firstRunAction' }))
    expect(onNavigate).toHaveBeenCalledWith('noderunner', { openTemplates: true })
  })

  it('pairs an unpaired bridge from the step and shows what happened', async () => {
    const PairExtension = vi.fn().mockResolvedValue(JSON.stringify({ opened: false, fallback: 'restart_bridge', hint: 'Restart the bridge, then pair again.', error: 'mismatch' }))
    window.go = { main: { App: { PairExtension } } }
    const onNavigate = vi.fn()
    const steps = getStartedSteps({ signedIn: true, okRuns: 0, bridge: 'unpaired', daemonRunning: false })
    render(<GetStartedCard steps={steps} bridge="unpaired" onNavigate={onNavigate} onDismiss={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'dashboard.getStarted.extensionAction' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'settings.browsers.pair' }))
    await waitFor(() => expect(PairExtension).toHaveBeenCalled())
    expect(await screen.findByText('settings.browsers.pairRestart')).toBeInTheDocument()
    expect(screen.getByText('Restart the bridge, then pair again.')).toBeInTheDocument()
    expect(onNavigate).not.toHaveBeenCalled()
  })

  it('sends a user with no bridge to Settings', () => {
    const onNavigate = vi.fn()
    const steps = getStartedSteps({ signedIn: true, okRuns: 0, bridge: 'off', daemonRunning: false })
    render(<GetStartedCard steps={steps} bridge="off" onNavigate={onNavigate} onDismiss={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'dashboard.getStarted.extensionAction' }))
    expect(onNavigate).toHaveBeenCalledWith('settings', { section: 'health' })
  })

  // The step has a pairing button now: the unpaired hint points at it, with
  // no command (or its backticks) to copy into a terminal.
  it('points an unpaired user at the pairing button', () => {
    for (const locale of [en, es]) {
      const text = locale.dashboard.getStarted.extensionWhyUnpaired
      expect(text).toMatch(/button|botón/)
      expect(text).not.toMatch(/`|monoagentcli|Settings|Ajustes/)
    }
  })

  it('opens sign-in and dismisses', () => {
    const onDismiss = vi.fn()
    const steps = getStartedSteps({ signedIn: false, okRuns: 0, bridge: 'off', daemonRunning: false })
    render(<GetStartedCard steps={steps} bridge="off" onNavigate={vi.fn()} onDismiss={onDismiss} />)
    fireEvent.click(screen.getByRole('button', { name: 'dashboard.getStarted.signInAction' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'dashboard.getStarted.dismiss' }))
    expect(onDismiss).toHaveBeenCalled()
  })
})
