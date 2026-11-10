// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'
import CoderModeSection from './CoderModeSection.jsx'

const App = {}
beforeEach(() => {
  for (const k of ['CoderStatus', 'CoderSetPeercredBinding']) App[k] = vi.fn()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const status = (over = {}) => JSON.stringify({ enabled: true, ready: true, workspaceRoot: '/w', runtime: 'claude', defaultAccess: 'scoped', ...over })
const SWITCH = 'Peer-credential binding'

describe('Peer-credential binding setting', () => {
  it('shows the switch off by default, with the risks written next to it', async () => {
    App.CoderStatus.mockResolvedValue(status())
    render(<CoderModeSection />)
    const sw = await screen.findByRole('switch', { name: SWITCH })
    expect(sw).not.toBeChecked()
    const risks = screen.getByTestId('coder-peercred-risks')
    for (const text of ['refused', 'no Coder turn is running', 'stay valid until they expire', 'cannot be switched off while an open-ended run', 'unattended']) {
      expect(risks).toHaveTextContent(text)
    }
  })

  it('switches it on through coder set --peercred-binding and shows the stored value', async () => {
    App.CoderStatus.mockResolvedValue(status())
    App.CoderSetPeercredBinding.mockResolvedValue(status({ peercredBinding: true }))
    render(<CoderModeSection />)
    fireEvent.click(await screen.findByRole('switch', { name: SWITCH }))
    await waitFor(() => expect(App.CoderSetPeercredBinding).toHaveBeenCalledWith(true))
    await waitFor(() => expect(screen.getByRole('switch', { name: SWITCH })).toBeChecked())
    expect(screen.getByRole('status')).toHaveTextContent('Saved.')
  })

  it('shows the CLI refusal in plain words and leaves the switch as it was', async () => {
    App.CoderStatus.mockResolvedValue(status())
    App.CoderSetPeercredBinding.mockResolvedValue(JSON.stringify({ error: 'peer-credential binding cannot be switched on: this machine cannot run it' }))
    render(<CoderModeSection />)
    fireEvent.click(await screen.findByRole('switch', { name: SWITCH }))
    expect(await screen.findByRole('alert')).toHaveTextContent('cannot be switched on: this machine cannot run it')
    expect(screen.getByRole('switch', { name: SWITCH })).not.toBeChecked()
  })
})
