// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import ConfirmHost from './ConfirmDialog.jsx'
import FixMonomindButton from './FixMonomindButton.jsx'

vi.mock('react-i18next', async () => {
  const en = (await import('../locales/en.json')).default
  const t = (key, o = {}) => String(key.split('.').reduce((n, k) => n?.[k], en) ?? key).replace(/{{(\w+)}}/g, (_, k) => o[k])
  return { useTranslation: () => ({ t, i18n: { language: 'en' } }) }
})

const mockRunFix = vi.fn()
vi.mock('../lib/health.js', () => ({ runFix: (...a) => mockRunFix(...a) }))

beforeEach(() => { mockRunFix.mockReset() })
afterEach(cleanup)

const mount = (onDone) => render(<><FixMonomindButton onDone={onDone} /><ConfirmHost /></>)

describe('FixMonomindButton', () => {
  it('names the command, and cancelling runs nothing', async () => {
    mount(vi.fn())
    fireEvent.click(screen.getByRole('button', { name: 'Fix monomind' }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('monoagentcli doctor fix monomind.install')
    expect(dialog).toHaveTextContent('npm install -g @monoes/monomindcli@latest')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(mockRunFix).not.toHaveBeenCalled()
  })

  it('confirming runs the CLI fix, shows progress, then rescans', async () => {
    let finish
    mockRunFix.mockImplementation((id, onLine) => new Promise(r => { onLine('installing'); finish = () => r({ ok: true }) }))
    const onDone = vi.fn()
    mount(onDone)
    fireEvent.click(screen.getByRole('button', { name: 'Fix monomind' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Run it' }))
    expect(await screen.findByText('installing')).toBeInTheDocument()
    expect(mockRunFix).toHaveBeenCalledWith('monomind.install', expect.any(Function))
    expect(onDone).not.toHaveBeenCalled()
    finish()
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('status')).toHaveTextContent('monomind fixed')
  })

  it('shows a failure and does not rescan', async () => {
    mockRunFix.mockResolvedValue({ ok: false, message: 'EACCES' })
    const onDone = vi.fn()
    mount(onDone)
    fireEvent.click(screen.getByRole('button', { name: 'Fix monomind' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Run it' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Fixing monomind failed: EACCES')
    expect(onDone).not.toHaveBeenCalled()
  })

  it('does not claim success when the rescan still cannot use monomind', async () => {
    mockRunFix.mockResolvedValue({ ok: true })
    const onDone = vi.fn().mockResolvedValue({ ready: false, missingCapabilities: [], probeError: 'shim still not found' })
    mount(onDone)
    fireEvent.click(screen.getByRole('button', { name: 'Fix monomind' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Run it' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Installed, but still: needs a working monomind (monomind couldn't be used: shim still not found)")
    expect(screen.queryByText(/monomind fixed/)).not.toBeInTheDocument()
  })

  it('a fix running elsewhere is not shown as a failure and keeps the button disabled', async () => {
    mockRunFix.mockResolvedValue({ ok: false, busy: true, message: 'already running' })
    mount(vi.fn())
    fireEvent.click(screen.getByRole('button', { name: 'Fix monomind' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Run it' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Already running elsewhere.')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Fix monomind' })).toBeDisabled()
  })

  it('the confirmation says an install may not fix a PATH or shim problem', async () => {
    mount(vi.fn())
    fireEvent.click(screen.getByRole('button', { name: 'Fix monomind' }))
    expect(await screen.findByRole('dialog')).toHaveTextContent('may not fix a PATH or version-manager (shim) problem')
  })
})

