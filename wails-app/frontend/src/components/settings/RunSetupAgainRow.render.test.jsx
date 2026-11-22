// @vitest-environment jsdom
// Settings > Application Info > Run setup again: restart, then open the onboarding.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'

const go = vi.hoisted(() => ({ RestartOnboarding: vi.fn(), GetOnboardingStateFull: vi.fn(() => Promise.resolve({ steps: [] })) }))
vi.mock('../../wailsjs/go/main/App', () => go)

import i18n from '../../i18n.js'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import RunSetupAgainRow from './RunSetupAgainRow.jsx'
import { setupSnapshot, readSetupState } from '../../pages/dashboard/setupSession.js'

let openOnboarding
const show = () => {
  openOnboarding = vi.fn(() => Promise.resolve())
  return render(<OnboardingContext.Provider value={{ openOnboarding, state: null, reload: vi.fn() }}><RunSetupAgainRow /></OnboardingContext.Provider>)
}

beforeEach(async () => {
  await i18n.changeLanguage('en')
  go.RestartOnboarding.mockReset()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('Run setup again', () => {
  it('restarts the onboarding, then opens it', async () => {
    go.RestartOnboarding.mockResolvedValue(undefined)
    await readSetupState() // the dashboard card read once this session
    expect(setupSnapshot().fresh).toBe(true)
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Run setup again' }))
    await waitFor(() => expect(openOnboarding).toHaveBeenCalledTimes(1))
    expect(go.RestartOnboarding).toHaveBeenCalledTimes(1)
    expect(go.RestartOnboarding.mock.invocationCallOrder[0]).toBeLessThan(openOnboarding.mock.invocationCallOrder[0])
    expect(openOnboarding).toHaveBeenCalledWith()
    // the dashboard card reads again once the onboarding closes
    expect(setupSnapshot().fresh).toBe(false)
  })

  it('a failed restart says why and opens nothing', async () => {
    go.RestartOnboarding.mockRejectedValue('gui state: permission denied')
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Run setup again' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('gui state: permission denied'))
    expect(openOnboarding).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Run setup again' })).toBeEnabled()
  })
})
