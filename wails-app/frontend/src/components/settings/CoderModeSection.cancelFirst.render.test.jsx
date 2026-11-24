// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import ConfirmHost from '../ConfirmDialog.jsx'
import CoderModeSection, { CODER_RISK_TITLE } from './CoderModeSection.jsx'

vi.mock('react-i18next', async () => {
  const en = (await import('../../locales/en.json')).default
  const t = (key, o = {}) => String(key.split('.').reduce((n, k) => n?.[k], en) ?? key).replace(/{{(\w+)}}/g, (_, k) => o[k])
  return { useTranslation: () => ({ t, i18n: { language: 'en' } }) }
})

const mockRunFix = vi.fn()
vi.mock('../../lib/health.js', () => ({ runFix: (...a) => mockRunFix(...a) }))

const App = {}
beforeEach(() => {
  mockRunFix.mockReset()
  for (const k of ['CoderStatus', 'CoderEnable', 'CoderDisable', 'CoderSet', 'PickCoderFolder']) App[k] = vi.fn()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const status = (over = {}) => JSON.stringify({
  enabled: false, workspaceRoot: '/home/u/monoagent-coder', maxTurns: 200, timeout: '60m', budgetUsd: 0,
  ready: true, missingCapabilities: [], monomindVersion: '1.2.3', runtime: 'claude', ...over,
})

function mount(st) {
  App.CoderStatus.mockResolvedValue(st)
  return render(<><CoderModeSection /><ConfirmHost /></>)
}

describe('CoderModeSection risk confirm is cancel-first', () => {
  it('focuses Cancel, and Enter alone does not turn Coder mode on', async () => {
    mount(status())
    const toggle = await screen.findByRole('checkbox', { name: 'Coder mode' })
    await waitFor(() => expect(toggle).not.toBeDisabled())
    fireEvent.click(toggle)
    await screen.findByRole('dialog', { name: CODER_RISK_TITLE })
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()
    fireEvent.keyDown(document.body, { key: 'Enter' })
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(App.CoderEnable).not.toHaveBeenCalled()
  })
})
