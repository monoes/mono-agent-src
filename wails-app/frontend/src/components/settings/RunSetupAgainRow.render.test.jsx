// @vitest-environment jsdom
// Settings > Application Info > Run setup again: restart, then open the onboarding.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'

const go = vi.hoisted(() => ({ RestartOnboarding: vi.fn() }))
vi.mock('../../wailsjs/go/main/App', () => go)

import i18n from '../../i18n.js'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import RunSetupAgainRow from './RunSetupAgainRow.jsx'

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
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Run setup again' }))
    await waitFor(() => expect(openOnboarding).toHaveBeenCalledTimes(1))
    expect(go.RestartOnboarding).toHaveBeenCalledTimes(1)
    expect(go.RestartOnboarding.mock.invocationCallOrder[0]).toBeLessThan(openOnboarding.mock.invocationCallOrder[0])
    expect(openOnboarding).toHaveBeenCalledWith()
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
