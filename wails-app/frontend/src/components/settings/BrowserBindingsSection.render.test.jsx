// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from '../../locales/en.json'
import es from '../../locales/es.json'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k) => k }) }))

const App = {}
beforeEach(() => {
  for (const k of ['GetBrowsers', 'BindBrowser', 'GetProfiles', 'GetExtensionStatus', 'PairExtension']) App[k] = vi.fn()
  window.go = { main: { App } }
  App.GetProfiles.mockResolvedValue([{ id: 'p-work', name: 'Work' }, { id: 'p-home', name: 'Personal' }])
})
afterEach(() => { cleanup(); delete window.go })

const edge = { instance: '3f2a91c0-0000-4000-8000-000000000001', label: 'Edge Work', profile_id: 'p-work', profile_name: 'Work', legacy: false, conflict: false, version: '1.5.0' }

async function mount(report) {
  if (report instanceof Error) App.GetBrowsers.mockRejectedValue(report)
  else App.GetBrowsers.mockResolvedValue(report)
  const { default: BrowserBindingsSection } = await import('./BrowserBindingsSection.jsx')
  render(<BrowserBindingsSection />)
  await screen.findByTestId('browser-bindings')
  await waitFor(() => expect(App.GetBrowsers).toHaveBeenCalled())
}

describe('BrowserBindingsSection', () => {
  it('shows each browser with its bound profile selected', async () => {
    await mount({ running: true, browsers: [edge] })
    const select = await screen.findByLabelText('Profile for Edge Work')
    expect(select).toHaveValue('p-work')
  })

  it('binds through the CLI when the select changes, then reloads', async () => {
    await mount({ running: true, browsers: [edge] })
    App.BindBrowser.mockResolvedValue({ ...edge, profile_id: 'p-home' })
    fireEvent.change(await screen.findByLabelText('Profile for Edge Work'), { target: { value: 'p-home' } })
    await waitFor(() => expect(App.BindBrowser).toHaveBeenCalledWith(edge.instance, 'p-home'))
    await waitFor(() => expect(App.GetBrowsers).toHaveBeenCalledTimes(2))
  })

  it('warns about a conflict and disables a legacy browser', async () => {
    await mount({ running: true, browsers: [
      { ...edge, conflict: true },
      { instance: 'legacy', label: '', profile_id: '', legacy: true },
    ] })
    expect(await screen.findByText(/only the most recently connected one is used/)).toBeInTheDocument()
    expect(screen.getByLabelText('Profile for legacy')).toBeDisabled()
  })

  it('says when no bridge is running', async () => {
    await mount({ running: false, hint: 'No bridge is running. Start one with `monoagentcli extension serve`.', browsers: [] })
    expect(await screen.findByText(/No bridge is running/)).toBeInTheDocument()
  })
})

describe('BrowserBindingsSection freshness', () => {
  it('re-reads when the window regains focus, so a bind made elsewhere shows up', async () => {
    await mount({ running: true, browsers: [edge] })
    App.GetBrowsers.mockResolvedValue({ running: true, browsers: [{ ...edge, profile_id: 'p-home', profile_name: 'Personal' }] })
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(screen.getByLabelText('Profile for Edge Work')).toHaveValue('p-home'))
  })
})

describe('BrowserBindingsSection pairing', () => {
  const mismatch = 'listing browsers: the bridge rejected this client: pairing token mismatch — run `monoagentcli extension status` / re-pair'

  it('turns a refused client into a "not paired" state with a Pair button, raw error under Details', async () => {
    App.GetExtensionStatus.mockResolvedValue(JSON.stringify({ running: true, bridge: { status: 'waiting' }, clientPairing: 'mismatch' }))
    await mount(new Error(mismatch))
    expect(await screen.findByText('settings.browsers.notPaired')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByText(mismatch).closest('details')).not.toBeNull()

    App.PairExtension.mockResolvedValue(JSON.stringify({ opened: false, fallback: 'restart_bridge', hint: 'Restart the bridge, then pair again.', error: 'bridge is running, but this client\'s pairing token doesn\'t match it' }))
    fireEvent.click(screen.getByRole('button', { name: 'settings.browsers.pair' }))
    expect(await screen.findByText('settings.browsers.pairRestart')).toBeInTheDocument()
    expect(screen.getByText('Restart the bridge, then pair again.')).toBeInTheDocument()
  })

  it('shows the hint\'s commands as code, not backticks', async () => {
    App.GetExtensionStatus.mockResolvedValue(JSON.stringify({ running: true, clientPairing: 'mismatch' }))
    await mount(new Error(mismatch))
    App.PairExtension.mockResolvedValue(JSON.stringify({ opened: false, fallback: 'token', hint: 'Run `monoagentcli extension pair` and paste the token.' }))
    fireEvent.click(await screen.findByRole('button', { name: 'settings.browsers.pair' }))
    const code = await screen.findByText('monoagentcli extension pair')
    expect(code.tagName).toBe('CODE')
    expect(screen.getByTestId('pair-result').textContent).not.toContain('`')
  })

  it('offers pairing when the extension itself is turned away, and says when the page opened', async () => {
    App.GetExtensionStatus.mockResolvedValue(JSON.stringify({ running: true, bridge: { status: 'unpaired' }, clientPairing: 'ok' }))
    await mount({ running: true, browsers: [] })
    App.PairExtension.mockResolvedValue(JSON.stringify({ opened: true, url: 'http://127.0.0.1:9222/monoagent/pair?n=abc', hint: 'The extension pairs itself.' }))
    fireEvent.click(await screen.findByRole('button', { name: 'settings.browsers.pair' }))
    expect(await screen.findByText('settings.browsers.pairOpened')).toBeInTheDocument()
    expect(screen.getByText('http://127.0.0.1:9222/monoagent/pair?n=abc')).toBeInTheDocument()
    expect(screen.queryByText(/No browser is attached/)).not.toBeInTheDocument()
  })

  it('keeps other errors as they are and does not ask for status when browsers are listed', async () => {
    App.GetExtensionStatus.mockResolvedValue(JSON.stringify({ running: true, bridge: { status: 'connected' }, clientPairing: 'ok' }))
    await mount(new Error('boom'))
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
    expect(screen.queryByText('settings.browsers.notPaired')).not.toBeInTheDocument()

    cleanup()
    App.GetExtensionStatus.mockClear()
    await mount({ running: true, browsers: [edge] })
    await screen.findByLabelText('Profile for Edge Work')
    expect(App.GetExtensionStatus).not.toHaveBeenCalled()
  })

  it('every settings.browsers key used exists in en and es, and both carry the same ones', () => {
    const src = ['BrowserBindingsSection.jsx', 'PairBrowser.jsx'].map(f => readFileSync(join(__dirname, f), 'utf8')).join('\n')
    const used = [...new Set([...src.matchAll(/['"`]settings\.browsers\.([a-zA-Z]+)['"`]/g)].map(m => m[1]))]
    expect(used.length).toBeGreaterThan(5)
    expect(Object.keys(es.settings.browsers).sort()).toEqual(Object.keys(en.settings.browsers).sort())
    expect(used.filter(k => !en.settings.browsers[k])).toEqual([])
    expect(Object.keys(en.settings.browsers).filter(k => !used.includes(k))).toEqual([])
  })
})
