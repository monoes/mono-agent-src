// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'

const quit = vi.hoisted(() => vi.fn())
const result = vi.hoisted(() => ({ value: null }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k) => k }) }))
vi.mock('../lib/health.js', () => ({
  getHealth: () => ({ report: null, cliMissing: false }),
  subscribeHealth: () => () => {},
  summarize: () => ({ level: 'ok', issues: 0 }),
}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1.0.0' }),
  CheckForUpdate: () => Promise.resolve({ update_available: true, latest_version: 'v9.9.9' }),
  AppSelfUpdate: () => Promise.resolve(result.value),
  GetActiveProfile: () => Promise.resolve({ id: 'work', name: 'Work' }),
  QuitApp: quit,
}))
vi.mock('../services/api.js', () => ({ subscribeEvent: () => () => {}, api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) } }))

import StatusBar from './StatusBar.jsx'

const NOTICE = 'daemon is down — start it with: monoagentcli daemon start\nThe update is installed; restart the app to use it.'

afterEach(() => { cleanup(); vi.clearAllMocks() })

async function install() {
  render(<StatusBar stats={{}} dbConnected={true} />)
  fireEvent.click(await screen.findByText(/MonoAgent ·/))
  fireEvent.click(await screen.findByText(/Install|Update$/i, { selector: 'button' }))
}

describe('StatusBar update notice', () => {
  it('shows the notice after an install that has one, and "Restart now" quits', async () => {
    result.value = { success: true, new_version: 'v9.9.9', notice: NOTICE }
    await install()
    const box = await screen.findByTestId('update-notice')
    expect(box).toHaveTextContent('daemon is down')
    expect(box).toHaveTextContent('restart the app to use it')
    expect(quit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('update-notice-restart'))
    expect(quit).toHaveBeenCalledTimes(1)
  })

  it('"Later" hides the notice but the bar still says the update waits for a restart', async () => {
    result.value = { success: true, new_version: 'v9.9.9', notice: NOTICE }
    await install()
    fireEvent.click(await screen.findByTestId('update-notice-later'))
    expect(screen.queryByTestId('update-notice')).toBeNull()
    expect(screen.getByText(/Updated to v9.9.9 — restart to apply/)).toBeInTheDocument()
    expect(quit).not.toHaveBeenCalled()
  })

  it('shows no notice box when the install has none', async () => {
    result.value = { success: true, new_version: 'v9.9.9' }
    await install()
    await screen.findByText(/Updated to v9.9.9/)
    expect(screen.queryByTestId('update-notice')).toBeNull()
  })
})
