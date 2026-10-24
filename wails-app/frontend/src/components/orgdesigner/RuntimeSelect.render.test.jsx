// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import RuntimeSelect from './RuntimeSelect.jsx'
import { sectionsOrgEnabled, designMeta, runtimeChoice, pickerRuntimes, resetSectionsRuntimePolicyCache } from './sectionsRuntimes.js'

const policy = {
  source: 'monomind',
  runtimes: [
    { runtime: 'kilo', status: 'refused', reason: 'Kilo supports full access only and a sections org refuses full access' },
    { runtime: 'qwen', status: 'unverified', reason: 'runtime "qwen" uses the generic "private-home" isolation' },
  ],
}
const orgSectionsRuntimes = vi.fn()
vi.mock('../../services/api.js', () => ({ api: { orgSectionsRuntimes: (...a) => orgSectionsRuntimes(...a) } }))
afterEach(() => { cleanup(); orgSectionsRuntimes.mockReset(); resetSectionsRuntimePolicyCache() })

const BASE = ['claude', 'qwen', 'codex']

function opt(name) { return screen.getByRole('option', { name: new RegExp(name, 'i') }) }

describe('RuntimeSelect in a sections org', () => {
  it('disables refused runtimes with monomind reason and allows unverified ones with a warning', async () => {
    orgSectionsRuntimes.mockResolvedValue(policy)
    const onChange = vi.fn()
    render(<RuntimeSelect value="" options={BASE} sectionsOrg onChange={onChange} />)
    await waitFor(() => expect(screen.getByRole('option', { name: /kilo/i })).toBeInTheDocument())
    expect(opt('kilo')).toBeDisabled()
    expect(opt('kilo')).toHaveAttribute('title', policy.runtimes[0].reason)
    expect(opt('qwen')).not.toBeDisabled()
    expect(opt('qwen').textContent).toMatch(/unverified/)
    expect(opt('claude')).not.toBeDisabled()
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'qwen' } })
    expect(onChange).toHaveBeenCalledWith('qwen')
  })

  it('shows monomind reason under a role already on a refused runtime', async () => {
    orgSectionsRuntimes.mockResolvedValue(policy)
    render(<RuntimeSelect value="kilo" options={BASE} sectionsOrg onChange={() => {}} />)
    const note = await screen.findByTestId('runtime-policy-note')
    expect(note).toHaveTextContent(policy.runtimes[0].reason)
  })

  it('warns, but does not block, on an unverified runtime', async () => {
    orgSectionsRuntimes.mockResolvedValue(policy)
    render(<RuntimeSelect value="qwen" options={BASE} sectionsOrg onChange={() => {}} />)
    const note = await screen.findByTestId('runtime-policy-note')
    expect(note).toHaveTextContent(/Unverified/)
    expect(screen.getByRole('combobox')).not.toBeDisabled()
  })

  it('leaves a plain org alone and never asks the CLI', () => {
    render(<RuntimeSelect value="" options={BASE} sectionsOrg={false} onChange={() => {}} />)
    expect(orgSectionsRuntimes).not.toHaveBeenCalled()
    expect(screen.queryByRole('option', { name: /kilo/i })).toBeNull()
  })

  it('blocks nothing when the CLI cannot answer', async () => {
    orgSectionsRuntimes.mockRejectedValue(new Error('offline'))
    render(<RuntimeSelect value="" options={BASE} sectionsOrg onChange={() => {}} />)
    await waitFor(() => expect(orgSectionsRuntimes).toHaveBeenCalled())
    expect(screen.queryByRole('option', { name: /kilo/i })).toBeNull()
  })
})

describe('policy cache', () => {
  it('asks the CLI once for several pickers, and again after a failure', async () => {
    orgSectionsRuntimes.mockResolvedValue(policy)
    const first = render(<RuntimeSelect value="" options={BASE} sectionsOrg onChange={() => {}} />)
    await waitFor(() => expect(screen.getByRole('option', { name: /kilo/i })).toBeInTheDocument())
    first.unmount()
    render(<RuntimeSelect value="" options={BASE} sectionsOrg onChange={() => {}} />)
    await waitFor(() => expect(screen.getByRole('option', { name: /kilo/i })).toBeInTheDocument())
    expect(orgSectionsRuntimes).toHaveBeenCalledTimes(1)
  })
  it('does not keep a failed answer', async () => {
    orgSectionsRuntimes.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(policy)
    const first = render(<RuntimeSelect value="" options={BASE} sectionsOrg onChange={() => {}} />)
    await waitFor(() => expect(orgSectionsRuntimes).toHaveBeenCalledTimes(1))
    await new Promise(r => setTimeout(r, 0))
    first.unmount()
    render(<RuntimeSelect value="" options={BASE} sectionsOrg onChange={() => {}} />)
    await waitFor(() => expect(screen.getByRole('option', { name: /kilo/i })).toBeInTheDocument())
    expect(orgSectionsRuntimes).toHaveBeenCalledTimes(2)
  })
})

describe('sections helpers', () => {
  it('sectionsOrgEnabled follows the flag Go sends, not the sections JSON', () => {
    expect(sectionsOrgEnabled(null)).toBe(false)
    expect(sectionsOrgEnabled(designMeta({ org: { roles: [], sections: { a: { lead: 'x' } } } }))).toBe(false)
    expect(sectionsOrgEnabled(designMeta({ org: { roles: [] }, sections_enabled: true }))).toBe(true)
    expect(designMeta({ org: { name: 'n', roles: [1] }, sections_enabled: false })).toEqual({ name: 'n', sectionsEnabled: false })
  })
  it('runtimeChoice and pickerRuntimes', () => {
    expect(runtimeChoice(policy, 'kilo').status).toBe('refused')
    expect(runtimeChoice(policy, 'claude').status).toBe('ok')
    expect(runtimeChoice(null, 'kilo').status).toBe('ok')
    expect(pickerRuntimes(BASE, policy)).toEqual([...BASE, 'kilo'])
  })
})

describe('org validate runtime findings', () => {
  it('reach the designer word for word through orgValidateReport', async () => {
    const { reportProblems } = await import('./orgValidateReport.js')
    const line = 'roles.a: runtime "kilo" is refused for sections orgs: Kilo supports full access only'
    expect(reportProblems({ valid: false, error: `${line}\nroles.c: runtime "qwen" uses the generic "private-home" isolation` }).errors[0]).toBe(line)
  })
})
