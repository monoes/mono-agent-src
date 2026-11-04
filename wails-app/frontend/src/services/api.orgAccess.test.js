import { describe, it, expect, vi } from 'vitest'

vi.mock('../wailsjs/go/main/App', () => ({
  ValidateOrgReport: vi.fn(),
  OrgRoleSetAccess: vi.fn(),
}))
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn() }))
import * as GoApp from '../wailsjs/go/main/App'
import { api } from './api.js'

describe('org full-access api wrappers (#205)', () => {
  it('validateOrgReport returns an invalid org\'s report instead of rejecting on its "error"', async () => {
    const report = { v: 1, org: 'growth', valid: false, warnings: [], error: 'taint: scraper → builder' }
    GoApp.ValidateOrgReport.mockResolvedValue(JSON.stringify(report))
    await expect(api.validateOrgReport('growth')).resolves.toEqual(report)
    GoApp.ValidateOrgReport.mockResolvedValue(JSON.stringify({ error: 'monomind not found' }))
    await expect(api.validateOrgReport('growth')).rejects.toThrow('monomind not found')
  })

  it('orgRoleSetAccess rejects with the CLI refusal verbatim, code included', async () => {
    GoApp.OrgRoleSetAccess.mockResolvedValue(JSON.stringify({ error: 'granting full access is human-only', code: 'operator_only' }))
    await expect(api.orgRoleSetAccess('growth', 'builder', 'full')).rejects.toMatchObject({ message: 'granting full access is human-only', code: 'operator_only' })
  })
})
