// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { StrictMode } from 'react'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { CollectionField, AnyPlatformConnections, CurlImport, resetPlatformNamesCache } from './NodeInspectorWidgets.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', () => ({ NodeConfigFromCurl: vi.fn(), PlatformNames: vi.fn() }))

afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('CollectionField', () => {
  const field = { key: 'options', label: 'Options', type: 'collection' }
  it('saves only valid JSON objects and keeps an object value an object', () => {
    const onCommit = vi.fn()
    render(<CollectionField field={field} value={{ timeout: 5 }} onCommit={onCommit} inputStyle={{}} />)
    const box = screen.getByLabelText('Options')
    expect(box.value).toBe('{\n  "timeout": 5\n}')
    fireEvent.change(box, { target: { value: '{"timeout": ' } })
    expect(onCommit).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('Not valid JSON')
    fireEvent.change(box, { target: { value: '{"timeout": 9}' } })
    expect(onCommit).toHaveBeenLastCalledWith({ timeout: 9 })
    expect(screen.queryByRole('alert')).toBeNull()
  })
  it('keeps a text value as text', () => {
    const onCommit = vi.fn()
    render(<CollectionField field={field} value="" onCommit={onCommit} inputStyle={{}} />)
    fireEvent.change(screen.getByLabelText('Options'), { target: { value: '{"a":1}' } })
    expect(onCommit).toHaveBeenLastCalledWith('{"a":1}')
  })
})

describe('AnyPlatformConnections', () => {
  const conns = [{ id: 'a', platform: 'stripe', label: 'Live' }, { id: 'b', platform: 'generic_api', label: 'work' }]
  afterEach(resetPlatformNamesCache)

  it('labels each platform group with its display name, read once', async () => {
    WailsApp.PlatformNames.mockResolvedValue({ generic_api: 'Generic API Key', stripe: 'Stripe' })
    render(<select aria-label="cred"><AnyPlatformConnections connections={conns} /></select>)
    await waitFor(() => expect([...document.querySelectorAll('optgroup')].map(g => g.label)).toEqual(['Generic API Key', 'Stripe']))
    cleanup()
    render(<select aria-label="cred"><AnyPlatformConnections connections={conns} /></select>)
    expect([...document.querySelectorAll('optgroup')].map(g => g.label)).toEqual(['Generic API Key', 'Stripe'])
    expect(WailsApp.PlatformNames).toHaveBeenCalledTimes(1)
  })

  it('falls back to the platform id when there is no name', async () => {
    WailsApp.PlatformNames.mockRejectedValue(new Error('no CLI'))
    render(<select aria-label="cred"><AnyPlatformConnections connections={conns} /></select>)
    await waitFor(() => expect(WailsApp.PlatformNames).toHaveBeenCalled())
    expect([...document.querySelectorAll('optgroup')].map(g => g.label)).toEqual(['generic_api', 'stripe'])
  })

  it('lists every platform as an option group', () => {
    WailsApp.PlatformNames.mockResolvedValue({})
    render(<select aria-label="cred"><AnyPlatformConnections connections={[
      { id: 'a', platform: 'stripe', label: 'Live' }, { id: 'b', platform: 'github', label: 'work' },
    ]} /></select>)
    const groups = document.querySelectorAll('optgroup')
    expect([...groups].map(g => g.label)).toEqual(['github', 'stripe'])
    expect(screen.getByRole('option', { name: 'Live' })).toHaveValue('a')
  })
})

describe('CurlImport', () => {
  it('imports through the CLI binding and shows its notes', async () => {
    WailsApp.NodeConfigFromCurl.mockResolvedValue({ config: { url: 'https://x.test', method: 'POST' }, notes: ['option --compressed ignored'] })
    const onImport = vi.fn()
    // StrictMode mounts twice, as the dev app does.
    render(<StrictMode><CurlImport onImport={onImport} inputStyle={{}} /></StrictMode>)
    fireEvent.click(screen.getByText('Import cURL…'))
    fireEvent.change(screen.getByLabelText('curl command'), { target: { value: 'curl -X POST https://x.test --compressed' } })
    fireEvent.click(screen.getByText(/^Import \(replaces/))
    await waitFor(() => expect(onImport).toHaveBeenCalledWith({ url: 'https://x.test', method: 'POST' }))
    expect(WailsApp.NodeConfigFromCurl).toHaveBeenCalledWith('curl -X POST https://x.test --compressed')
    expect(screen.getByText('option --compressed ignored')).toBeInTheDocument()
  })
  it('shows the CLI error and imports nothing', async () => {
    WailsApp.NodeConfigFromCurl.mockResolvedValue({ config: null, notes: [], error: 'invalid input: not a curl command' })
    const onImport = vi.fn()
    render(<CurlImport onImport={onImport} inputStyle={{}} />)
    fireEvent.click(screen.getByText('Import cURL…'))
    fireEvent.change(screen.getByLabelText('curl command'), { target: { value: 'wget x' } })
    fireEvent.click(screen.getByText(/^Import \(replaces/))
    expect(await screen.findByRole('alert')).toHaveTextContent('not a curl command')
    expect(onImport).not.toHaveBeenCalled()
  })
})
