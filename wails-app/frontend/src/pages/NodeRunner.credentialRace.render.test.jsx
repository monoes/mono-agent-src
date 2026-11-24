// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import '../i18n.js'
import { render, screen, cleanup, act } from '@testing-library/react'
import { Inspector } from './NodeRunner.jsx'
import { api } from '../services/api.js'

vi.mock('../wailsjs/go/main/App', async (importOriginal) => ({
  ...await importOriginal(),
  GetVaultImages: vi.fn(() => Promise.resolve([])),
}))
vi.mock('../services/api.js', () => ({
  api: { getConnectionsForPlatform: vi.fn(), listConnections: vi.fn() },
  notify: vi.fn(), reportRunFailure: vi.fn(), subscribeEvent: vi.fn(() => () => {}),
}))

afterEach(() => { cleanup(); vi.clearAllMocks() })

const mk = (id, platform) => ({
  id, subtype: `${platform}.act`, label: id, category: 'social', config: {},
  schema: { credential_platform: platform, fields: [] },
})
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const props = { onConfigChange: vi.fn(), onClose: vi.fn(), liveSchemas: {}, automationIds: new Set() }

describe('Inspector credential list (R21-08)', () => {
  it('a slow load for the previous node cannot overwrite the current node or clear its loading flag', async () => {
    const a = deferred(), b = deferred()
    api.getConnectionsForPlatform.mockImplementation(p => (p === 'pa' ? a.promise : b.promise))
    const { rerender } = render(<Inspector node={mk('n1', 'pa')} {...props} />)
    rerender(<Inspector node={mk('n2', 'pb')} {...props} />)
    // The current node's request is still pending: the earlier one lands first.
    await act(async () => { a.resolve([{ id: 'from-a', label: 'A cred' }]) })
    expect(screen.queryByText(/A cred/)).toBeNull()
    expect(screen.getByText('Loading…')).toBeInTheDocument()
    await act(async () => { b.resolve([{ id: 'from-b', label: 'B cred' }]) })
    expect(screen.getByText(/B cred/)).toBeInTheDocument()
    expect(screen.queryByText(/A cred/)).toBeNull()
    expect(screen.queryByText('Loading…')).toBeNull()
  })
})
