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
  for (const k of ['GetBrowsers', 'AssignBrowser', 'GetProfiles', 'GetExtensionStatus', 'PairExtension']) App[k] = vi.fn()
  window.go = { main: { App } }
  App.GetProfiles.mockResolvedValue([
    { id: 'p-work', name: 'Work', is_active: true },
    { id: 'p-home', name: 'Personal', is_active: false },
  ])
})
afterEach(() => { cleanup(); delete window.go })

const edge = { instance: '3f2a91c0-0000-4000-8000-000000000001', label: 'Edge Work', profile_id: 'p-work', profile_ids: ['p-work'], legacy: false, conflict: false, version: '1.8.0' }
const chrome = { instance: '9b7c11d2-0000-4000-8000-000000000002', label: 'Chrome Home', profile_id: '', profile_ids: [], legacy: false, conflict: false, version: '1.8.0' }

async function mount(report) {
  if (report instanceof Error) App.GetBrowsers.mockRejectedValue(report)
  else App.GetBrowsers.mockResolvedValue(report)
  const { default: BrowserBindingsSection } = await import('./BrowserBindingsSection.jsx')
  render(<BrowserBindingsSection />)
  await screen.findByTestId('browser-bindings')
  await waitFor(() => expect(App.GetBrowsers).toHaveBeenCalled())
}

describe('BrowserBindingsSection', () => {
  it('lists one row per profile with the browser that runs it selected', async () => {
    await mount({ running: true, browsers: [edge, chrome] })
    expect(await screen.findByText('Work (active)')).toBeInTheDocument()
    expect(screen.getByLabelText('Browser for Work')).toHaveValue(edge.instance)
    expect(screen.getByLabelText('Browser for Personal')).toHaveValue('')
    const options = Array.from(screen.getByLabelText('Browser for Personal').querySelectorAll('option')).map((o) => o.textContent)
    expect(options).toEqual(['Any browser (default)', 'Edge Work · 3f2a91c0', 'Chrome Home · 9b7c11d2'])
  })

  it('assigns through the CLI when the select changes, then reloads', async () => {
    await mount({ running: true, browsers: [edge, chrome] })
    App.AssignBrowser.mockResolvedValue({ profile_id: 'p-home', instance: edge.instance, changed: [] })
    fireEvent.change(await screen.findByLabelText('Browser for Personal'), { target: { value: edge.instance } })
    await waitFor(() => expect(App.AssignBrowser).toHaveBeenCalledWith('p-home', edge.instance))
    await waitFor(() => expect(App.GetBrowsers).toHaveBeenCalledTimes(2))
  })

  it('choosing "Any browser" assigns no browser', async () => {
    await mount({ running: true, browsers: [edge, chrome] })
    App.AssignBrowser.mockResolvedValue({ profile_id: 'p-work', instance: '', changed: [] })
    fireEvent.change(await screen.findByLabelText('Browser for Work'), { target: { value: '' } })
    await waitFor(() => expect(App.AssignBrowser).toHaveBeenCalledWith('p-work', ''))
  })

  it('lets several profiles share one browser without a warning, and counts them', async () => {
    const shared = { ...edge, profile_ids: ['p-work', 'p-home'] }
    await mount({ running: true, browsers: [shared, chrome] })
    expect(await screen.findByLabelText('Browser for Work')).toHaveValue(shared.instance)
    expect(screen.getByLabelText('Browser for Personal')).toHaveValue(shared.instance)
    expect(screen.queryByText(/only the most recently connected one is used/)).toBeNull()
    expect(screen.getByText(/Edge Work · 3f2a91c0 · extension 1\.8\.0: 2 profiles use this browser/)).toBeInTheDocument()
    expect(screen.getByText(/Chrome Home · 9b7c11d2 · extension 1\.8\.0: no profiles of its own/)).toBeInTheDocument()
  })

  it('reads an older CLI that reports only profile_id', async () => {
    await mount({ running: true, browsers: [{ ...edge, profile_ids: undefined }, chrome] })
    expect(await screen.findByLabelText('Browser for Work')).toHaveValue(edge.instance)
    expect(screen.getByText(/1 profile uses this browser/)).toBeInTheDocument()
  })

  it('warns when one profile is on two browsers and shows the newest', async () => {
    await mount({ running: true, browsers: [{ ...chrome, profile_ids: ['p-work'], conflict: true }, { ...edge, conflict: true }] })
    expect(await screen.findByText(/only the most recently connected one is used/)).toBeInTheDocument()
    expect(screen.getByLabelText('Browser for Work')).toHaveValue(chrome.instance)
  })

  it('disables a legacy browser option and says why', async () => {
    await mount({ running: true, browsers: [edge, { instance: 'legacy', label: '', profile_id: '', profile_ids: [], legacy: true }] })
    await screen.findByLabelText('Browser for Work')
    const opt = screen.getAllByRole('option', { name: /Browser · legacy/ })[0]
    expect(opt).toBeDisabled()
    expect(screen.getByText(/too old to bind\. Reload it/)).toBeInTheDocument()
  })

  it('shows the error when assigning fails', async () => {
    await mount({ running: true, browsers: [edge, chrome] })
    App.AssignBrowser.mockRejectedValue(new Error('browser "x" is not attached to the bridge'))
    fireEvent.change(await screen.findByLabelText('Browser for Personal'), { target: { value: chrome.instance } })
    expect(await screen.findByRole('alert')).toHaveTextContent('is not attached')
  })

  it('says when no bridge is running', async () => {
    await mount({ running: false, hint: 'No bridge is running. Start one with `monoagentcli extension serve`.', browsers: [] })
    expect(await screen.findByText(/No bridge is running/)).toBeInTheDocument()
  })
})

describe('BrowserBindingsSection freshness', () => {
  it('re-reads when the window regains focus, so a change made elsewhere shows up', async () => {
    await mount({ running: true, browsers: [edge, chrome] })
    App.GetBrowsers.mockResolvedValue({ running: true, browsers: [{ ...edge, profile_ids: [] }, { ...chrome, profile_ids: ['p-work'] }] })
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(screen.getByLabelText('Browser for Work')).toHaveValue(chrome.instance))
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
    await mount({ running: true, browsers: [edge, chrome] })
    await screen.findByLabelText('Browser for Work')
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
