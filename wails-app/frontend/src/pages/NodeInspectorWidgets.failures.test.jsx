// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { AnyPlatformConnections, CurlImport, resetPlatformNamesCache } from './NodeInspectorWidgets.jsx'
import { api } from '../services/api.js'

// api methods that REJECT (the real ones swallow errors today; a future
// change, or a different caller, must not strand the widgets).
vi.mock('../services/api.js', () => ({
  api: { nodeConfigFromCurl: vi.fn(), platformNames: vi.fn() },
  notify: vi.fn(),
}))

afterEach(() => { cleanup(); resetPlatformNamesCache(); vi.clearAllMocks() })

describe('CurlImport failure', () => {
  it('a throwing import reports the error and frees the button', async () => {
    api.nodeConfigFromCurl.mockRejectedValue(new Error('bridge down'))
    const onImport = vi.fn()
    render(<CurlImport onImport={onImport} inputStyle={{}} />)
    fireEvent.click(screen.getByText('Import cURL…'))
    fireEvent.change(screen.getByLabelText('curl command'), { target: { value: 'curl x' } })
    fireEvent.click(screen.getByText(/^Import \(replaces/))
    expect(await screen.findByRole('alert')).toHaveTextContent('bridge down')
    expect(screen.getByText(/^Import \(replaces/)).toBeEnabled()
    expect(onImport).not.toHaveBeenCalled()
  })
})

describe('platform names cache', () => {
  const conns = [{ id: 'a', platform: 'stripe', label: 'Live' }]
  it('a rejected read is not kept: the next picker asks again', async () => {
    api.platformNames.mockRejectedValueOnce(new Error('no CLI')).mockResolvedValue({ stripe: 'Stripe' })
    render(<select><AnyPlatformConnections connections={conns} /></select>)
    await waitFor(() => expect(api.platformNames).toHaveBeenCalledTimes(1))
    await new Promise(r => setTimeout(r, 0))
    cleanup()
    render(<select><AnyPlatformConnections connections={conns} /></select>)
    await waitFor(() => expect(api.platformNames).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(document.querySelector('optgroup').label).toBe('Stripe'))
  })
})
