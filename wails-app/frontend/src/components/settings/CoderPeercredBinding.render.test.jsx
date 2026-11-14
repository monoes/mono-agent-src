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

const status = (over = {}) => JSON.stringify({ enabled: true, ready: true, workspaceRoot: '/w', runtime: 'claude', defaultAccess: 'full', ...over })

describe('Peer-credential binding', () => {
  it('shows the default as on, from the effective value (a revert to off-by-default fails this)', async () => {
    App.CoderStatus.mockResolvedValue(status({ peercredBindingEffective: true, peercredBindingSource: 'default' }))
    render(<CoderModeSection />)
    expect(await screen.findByLabelText('Peer-credential binding')).toBeChecked()
  })

  it('says why it is off where the machine cannot run it, and keeps an explicit off off', async () => {
    App.CoderStatus.mockResolvedValue(status({ peercredBindingEffective: false, peercredBindingSource: 'default', peercredBindingNote: 'on by default, but this machine cannot run it (old kernel), so it stays off here' }))
    render(<CoderModeSection />)
    expect(await screen.findByLabelText('Peer-credential binding')).not.toBeChecked()
    expect(screen.getByTestId('coder-binding-unavailable')).toHaveTextContent('old kernel')
    cleanup()
    App.CoderStatus.mockResolvedValue(status({ peercredBinding: false, peercredBindingEffective: false, peercredBindingSource: 'set' }))
    render(<CoderModeSection />)
    expect(await screen.findByLabelText('Peer-credential binding')).not.toBeChecked()
    expect(screen.queryByTestId('coder-binding-unavailable')).toBeNull()
  })

  it('is off by default and shows the risk warning with every point', async () => {
    App.CoderStatus.mockResolvedValue(status())
    render(<CoderModeSection />)
    expect(await screen.findByLabelText('Peer-credential binding')).not.toBeChecked()
    const w = screen.getByTestId('coder-binding-warning')
    for (const part of ['cannot run the binding service', 'no coder turn is running', 'stay valid until they expire', 'cannot be switched off while an open-ended run is open', 'autonomous agent', 'inside a coder turn']) {
      expect(w).toHaveTextContent(part)
    }
  })

  it('switches it on through coder set --peercred-binding and shows the returned value', async () => {
    App.CoderStatus.mockResolvedValue(status())
    App.CoderSetPeercredBinding.mockResolvedValue(status({ peercredBinding: true }))
    render(<CoderModeSection />)
    fireEvent.click(await screen.findByLabelText('Peer-credential binding'))
    await waitFor(() => expect(App.CoderSetPeercredBinding).toHaveBeenCalledWith(true))
    await waitFor(() => expect(screen.getByLabelText('Peer-credential binding')).toBeChecked())
    expect(screen.getByRole('status')).toHaveTextContent('Peer-credential binding is on.')
  })

  it('switches it off again', async () => {
    App.CoderStatus.mockResolvedValue(status({ peercredBinding: true }))
    App.CoderSetPeercredBinding.mockResolvedValue(status())
    render(<CoderModeSection />)
    const box = await screen.findByLabelText('Peer-credential binding')
    expect(box).toBeChecked()
    fireEvent.click(box)
    await waitFor(() => expect(App.CoderSetPeercredBinding).toHaveBeenCalledWith(false))
    await waitFor(() => expect(screen.getByLabelText('Peer-credential binding')).not.toBeChecked())
  })

  it('shows the backend refusal in its own words and leaves the box as it was', async () => {
    App.CoderStatus.mockResolvedValue(status())
    App.CoderSetPeercredBinding.mockRejectedValue(new Error('peer-credential binding cannot be switched on while 1 coder turn(s) are running without it'))
    render(<CoderModeSection />)
    fireEvent.click(await screen.findByLabelText('Peer-credential binding'))
    expect(await screen.findByRole('alert')).toHaveTextContent('cannot be switched on while 1 coder turn(s) are running')
    expect(screen.getByLabelText('Peer-credential binding')).not.toBeChecked()
  })
})
