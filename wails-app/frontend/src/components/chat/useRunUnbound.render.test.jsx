// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'
import { CoderHeader } from './CoderHeader.jsx'
import { useRunUnbound } from './useRunUnbound.js'

afterEach(() => cleanup())
const W = 'running without peer-credential binding: this machine cannot run it (old kernel)'

function Probe({ api, runExists }) {
  const w = useRunUnbound(runExists, api)
  return <CoderHeader cwd="/w" chatMode={{ state: { mode: 'open' } }} openEnded={{ runExists, unboundWarning: w }} />
}

describe('the unbound run chip', () => {
  it('shows the CLI warning in the chat header while the run is unbound', async () => {
    const api = { status: vi.fn().mockResolvedValue({ active: true, warning: W }) }
    render(<Probe api={api} runExists />)
    expect(await screen.findByTestId('open-unbound-chip')).toHaveTextContent(W)
  })
  it('shows nothing for a bound run, or when no run exists', async () => {
    const api = { status: vi.fn().mockResolvedValue({ active: true }) }
    render(<Probe api={api} runExists />)
    await waitFor(() => expect(api.status).toHaveBeenCalled())
    expect(screen.queryByTestId('open-unbound-chip')).toBeNull()
    cleanup()
    const api2 = { status: vi.fn().mockResolvedValue({ active: true, warning: W }) }
    render(<Probe api={api2} runExists={false} />)
    expect(api2.status).not.toHaveBeenCalled()
    expect(screen.queryByTestId('open-unbound-chip')).toBeNull()
  })
})
