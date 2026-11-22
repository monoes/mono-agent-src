import { describe, it, expect, vi, beforeEach } from 'vitest'

vi.mock('../wailsjs/go/main/App', () => ({ OpenURL: vi.fn() }))
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn() }))

import * as GoApp from '../wailsjs/go/main/App'
import { api } from './api.js'

describe('api.openURL scheme check', () => {
  beforeEach(() => { GoApp.OpenURL.mockReset(); GoApp.OpenURL.mockResolvedValue(undefined) })

  it('opens http, https and mailto links', async () => {
    await api.openURL('https://example.com/a')
    await api.openURL('http://example.com')
    await api.openURL('mailto:a@example.com')
    expect(GoApp.OpenURL).toHaveBeenCalledTimes(3)
  })

  it('never hands file:, javascript:, custom schemes or junk to the host', async () => {
    for (const u of ['file:///etc/passwd', 'javascript:alert(1)', 'vscode://x', 'ms-msdt:/id', '', null, undefined, 'not a url'])
      await api.openURL(u)
    expect(GoApp.OpenURL).not.toHaveBeenCalled()
  })
})
