// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within, act } from '@testing-library/react'
import AutonomyBar from './AutonomyBar.jsx'
import { api, notify } from '../../services/api.js'
// The real i18n setup, as main.jsx loads it, so the bar's labels come from
// src/locales/*.json and the English names below stay findable.
import i18n from '../../i18n.js'
import en from '../../locales/en.json'
import es from '../../locales/es.json'

vi.mock('../../services/api.js', () => ({
  api: {
    getOrgAutonomy: vi.fn(),
    setOrgAutonomy: vi.fn(),
    pauseOrgAutonomy: vi.fn(),
    resumeOrgAutonomy: vi.fn(),
    jevProviders: vi.fn(),
    listOrgGrants: vi.fn(),
    getOrgGates: vi.fn(),
  },
  notify: vi.fn(),
}))

const AUTONOMY = {
  v: 1, org: 'growth', level: 'mid', effective_level: 'mid', paused_until: null,
  decider: { kind: 'model', runtime: 'claude', model: 'm', fallback: 'model', timeout_seconds: 120 },
  tiers: {}, default_tiers: { gate: 'irreversible' }, policy: '', on_decider_failure: 'deny',
  limits: {}, daemon_running: true,
}

beforeEach(async () => {
  vi.clearAllMocks()
  await i18n.changeLanguage('en')
  api.getOrgAutonomy.mockResolvedValue(AUTONOMY)
  api.setOrgAutonomy.mockResolvedValue({ v: 1 })
  api.pauseOrgAutonomy.mockResolvedValue({ v: 1 })
  api.resumeOrgAutonomy.mockResolvedValue({ v: 1 })
  // No provider configured: only the built-in TypeSafe one.
  api.jevProviders.mockResolvedValue({ default: 'typesafe', kinds: ['openai'], providers: [{ name: 'typesafe', kind: 'typesafe', builtin: true }] })
  api.listOrgGrants.mockResolvedValue({
    v: 1, org: 'growth', grants: [
      { role: 'lead', alias: 'publish_post', workflow_name: 'Publish post', tier: 'irreversible', approval: 'required' },
      { role: 'lead', alias: 'summarize', workflow_name: 'Summarize', tier: 'consequential', approval: 'required' },
    ],
  })
  api.getOrgGates.mockResolvedValue({ gates: [{ id: 'gate-1', name: 'publish-launch-post', roleId: 'judge', status: 'pending' }] })
})
afterEach(() => cleanup())

async function renderBar() {
  render(<React.StrictMode><AutonomyBar orgName="growth" /></React.StrictMode>)
  await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Autonomy level' })).toBeInTheDocument())
  // StrictMode runs the bar's load effect twice, so the radiogroup can appear
  // while the second getOrgAutonomy is still settling. Interacting in that
  // window let its update (and the threshold field's effects) land on top of
  // a fireEvent.change and revert the typed value — the "saves the Jev
  // threshold" flake under CPU load. Wait for every load this render started
  // to resolve, inside act, so the bar is settled before the test acts.
  await act(async () => { await Promise.all(api.getOrgAutonomy.mock.results.map(r => r.value)) })
}

describe('AutonomyBar', () => {
  it('shows the current level and decider', async () => {
    await renderBar()
    expect(screen.getByRole('radio', { name: 'Mid' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'Manual' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByLabelText('Decider')).toHaveValue('model')
  })

  it('writes a lower level through org autonomy set', async () => {
    await renderBar()
    fireEvent.click(screen.getByRole('radio', { name: 'Manual' }))
    await waitFor(() => expect(api.setOrgAutonomy).toHaveBeenCalledWith('growth', { level: 'manual' }))
    await waitFor(() => expect(api.getOrgAutonomy.mock.calls.length).toBeGreaterThanOrEqual(3))
  })

  it('asks before full auto and lists gates and irreversible grants', async () => {
    await renderBar()
    fireEvent.click(screen.getByRole('radio', { name: 'Full auto' }))
    const dialog = await screen.findByRole('dialog', { name: 'Turn on full auto' })
    await waitFor(() => expect(within(dialog).getByText(/Every gate a role raises/)).toBeInTheDocument())
    expect(within(dialog).getByText(/publish-launch-post \(judge\)/)).toBeInTheDocument()
    expect(within(dialog).getByText('Publish post')).toBeInTheDocument()
    expect(within(dialog).queryByText('Summarize')).not.toBeInTheDocument()
    expect(api.setOrgAutonomy).not.toHaveBeenCalled()

    fireEvent.click(within(dialog).getByRole('button', { name: 'Turn on full auto' }))
    await waitFor(() => expect(api.setOrgAutonomy).toHaveBeenCalledWith('growth', { level: 'full' }))
  })

  it('cancelling the full auto confirmation changes nothing', async () => {
    await renderBar()
    fireEvent.click(screen.getByRole('radio', { name: 'Full auto' }))
    const dialog = await screen.findByRole('dialog', { name: 'Turn on full auto' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.setOrgAutonomy).not.toHaveBeenCalled()
  })

  it('changes the decider', async () => {
    await renderBar()
    fireEvent.change(screen.getByLabelText('Decider'), { target: { value: 'boss' } })
    await waitFor(() => expect(api.setOrgAutonomy).toHaveBeenCalledWith('growth', { decider: { kind: 'boss' } }))
  })

  it.each([
    ['For 30 min', '30m'],
    ['For 2 hours', '2h'],
    ['Until resumed', ''],
  ])('pauses autonomy %s', async (label, duration) => {
    await renderBar()
    fireEvent.click(screen.getByRole('button', { name: /Pause autonomy/ }))
    fireEvent.click(screen.getByRole('menuitem', { name: label }))
    await waitFor(() => expect(api.pauseOrgAutonomy).toHaveBeenCalledWith('growth', duration))
  })

  it('shows a paused org with Resume', async () => {
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, paused_until: '2999-01-01T00:00:00Z', effective_level: 'manual' })
    await renderBar()
    expect(screen.getByText('Paused')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Resume/ }))
    await waitFor(() => expect(api.resumeOrgAutonomy).toHaveBeenCalledWith('growth'))
  })

  it('warns when the daemon is not running', async () => {
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, daemon_running: false })
    await renderBar()
    expect(screen.getByText('Daemon off: acts as manual')).toBeInTheDocument()
  })

  it('toasts a failed change', async () => {
    api.setOrgAutonomy.mockResolvedValue({ error: 'boss decider needs monomind with capability "org-tool-providers"' })
    await renderBar()
    fireEvent.change(screen.getByLabelText('Decider'), { target: { value: 'boss' } })
    await waitFor(() => expect(notify).toHaveBeenCalledWith('set decider', expect.stringContaining('org-tool-providers')))
  })

  it('shows an unavailable state when the CLI cannot read autonomy', async () => {
    api.getOrgAutonomy.mockResolvedValue({ error: 'unknown command "autonomy"' })
    render(<AutonomyBar orgName="growth" />)
    expect(await screen.findByText('Autonomy unavailable')).toBeInTheDocument()
  })

  it('has every string in both locales', () => {
    for (const ns of ['autonomy', 'fullAuto']) {
      const walk = (a, b, path) => {
        for (const [k, v] of Object.entries(a)) {
          if (typeof v === 'object') walk(v, b?.[k], `${path}.${k}`)
          else expect(typeof b?.[k], `es is missing ${path}.${k}`).toBe('string')
        }
      }
      walk(en.orgs[ns], es.orgs[ns], `orgs.${ns}`)
      walk(es.orgs[ns], en.orgs[ns], `orgs.${ns}`)
    }
  })

  it('speaks the chosen language, including the Full auto confirmation', async () => {
    await act(() => i18n.changeLanguage('es'))
    render(<AutonomyBar orgName="growth" />)
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: es.orgs.autonomy.levelGroup })).toBeInTheDocument())
    expect(screen.getByRole('radio', { name: es.orgs.autonomy.levels.mid })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByLabelText(es.orgs.autonomy.decider)).toHaveValue('model')
    expect(screen.getByRole('button', { name: new RegExp(es.orgs.autonomy.pause) })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('radio', { name: es.orgs.autonomy.levels.full }))
    const dialog = await screen.findByRole('dialog', { name: es.orgs.fullAuto.dialog })
    await waitFor(() => expect(within(dialog).getByText(new RegExp(es.orgs.fullAuto.everyGate))).toBeInTheDocument())
    expect(within(dialog).getByRole('button', { name: es.orgs.fullAuto.confirm })).toBeInTheDocument()
  })

  it('shows the Jev threshold field only for the jev decider', async () => {
    await renderBar()
    expect(screen.queryByLabelText('Jev ≥')).not.toBeInTheDocument()
    expect(screen.queryByTestId('jev-decider-note')).not.toBeInTheDocument()
    cleanup()
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', threshold: 0.8 } })
    await renderBar()
    const field = screen.getByLabelText('Jev ≥')
    expect(field).toHaveValue(0.8)
    expect(field).toHaveAttribute('min', '0.05')
    expect(field).toHaveAttribute('max', '1')
    expect(field).toHaveAttribute('step', '0.05')
    const note = screen.getByTestId('jev-decider-note')
    expect(note).toHaveTextContent('Needs a TypeSafe key')
    expect(note).toHaveTextContent('Settings › TypeSafe Jev')
    expect(note).toHaveTextContent('questions and p < 0.8 go to the model decider (claude · m)')
  })

  it('saves the Jev threshold through org autonomy set', async () => {
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', threshold: 0.8 } })
    await renderBar()
    const field = screen.getByLabelText('Jev ≥')
    fireEvent.change(field, { target: { value: '0.9' } })
    fireEvent.blur(field)
    await waitFor(() => expect(api.setOrgAutonomy).toHaveBeenCalledWith('growth', { decider: { kind: 'jev', threshold: 0.9 } }))
  })

  it('rejects an out-of-range Jev threshold without saving', async () => {
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', threshold: 0.8 } })
    await renderBar()
    const field = screen.getByLabelText('Jev ≥')
    fireEvent.change(field, { target: { value: '1.5' } })
    fireEvent.blur(field)
    expect(field).toHaveValue(0.8)
    expect(api.setOrgAutonomy).not.toHaveBeenCalled()
  })

  it('links the Jev note to Settings when a navigator is given', async () => {
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev' } })
    const open = vi.fn()
    render(<AutonomyBar orgName="growth" onOpenJevSettings={open} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Settings › TypeSafe Jev' }))
    expect(open).toHaveBeenCalled()
    // No threshold stored yet: the field shows the CLI default.
    expect(screen.getByLabelText('Jev ≥')).toHaveValue(0.8)
  })

  it('reads providers only for the jev decider, and shows no select with only TypeSafe', async () => {
    await renderBar()
    expect(api.jevProviders).not.toHaveBeenCalled()
    cleanup()
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', provider: '' } })
    await renderBar()
    await waitFor(() => expect(api.jevProviders).toHaveBeenCalled())
    expect(screen.queryByLabelText('Provider')).not.toBeInTheDocument()
    expect(screen.getByTestId('jev-decider-note')).toHaveTextContent('Needs a TypeSafe key')
  })

  it('picks the jev decider provider, and Default clears it', async () => {
    api.jevProviders.mockResolvedValue({ default: 'typesafe', kinds: ['openai'], providers: [
      { name: 'typesafe', kind: 'typesafe', builtin: true }, { name: 'local', kind: 'openai', builtin: false },
    ] })
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', provider: '' } })
    await renderBar()
    const sel = await screen.findByLabelText('Provider')
    expect(sel).toHaveValue('')
    expect(within(sel).getAllByRole('option').map(o => o.textContent)).toEqual(['Default', 'typesafe', 'local'])
    fireEvent.change(sel, { target: { value: 'local' } })
    await waitFor(() => expect(api.setOrgAutonomy).toHaveBeenCalledWith('growth', { decider: { kind: 'jev', provider: 'local' } }))
    cleanup()
    api.setOrgAutonomy.mockClear()
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', provider: 'local' } })
    await renderBar()
    const sel2 = await screen.findByLabelText('Provider')
    await waitFor(() => expect(sel2).toHaveValue('local'))
    // A provider other than TypeSafe needs no TypeSafe key.
    expect(screen.getByTestId('jev-decider-note')).not.toHaveTextContent('Needs a TypeSafe key')
    fireEvent.change(sel2, { target: { value: '' } })
    await waitFor(() => expect(api.setOrgAutonomy).toHaveBeenCalledWith('growth', { decider: { kind: 'jev', provider: '' } }))
  })

  // An unset decider provider resolves through the profile's decider choice
  // before the machine default, and neither JSON reports that choice: the
  // option must not name the machine default as if it were the answer.
  it('labels the unset provider "Default" without naming the machine default', async () => {
    api.jevProviders.mockResolvedValue({ default: 'local', kinds: ['openai'], providers: [
      { name: 'typesafe', kind: 'typesafe', builtin: true }, { name: 'local', kind: 'openai', builtin: false },
    ] })
    api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, decider: { ...AUTONOMY.decider, kind: 'jev', provider: '' } })
    await renderBar()
    const sel = await screen.findByLabelText('Provider')
    const first = within(sel).getAllByRole('option')[0]
    expect(first).toHaveValue('')
    expect(first).toHaveTextContent(/^Default$/)
    expect(within(sel).queryByText('Default (local)')).not.toBeInTheDocument()
    expect(sel.closest('label')).toHaveAttribute('title', expect.stringContaining('else the machine default'))
  })

  // A sampled provider's verdict reaches at most 0.75 on mid's three
  // verdicts and 6/7 on full's two (5 answers, add-one smoothed).
  describe('a sampled decider provider', () => {
    const sampled = (level, threshold) => {
      api.jevProviders.mockResolvedValue({ default: 'typesafe', kinds: ['anthropic'], providers: [
        { name: 'typesafe', kind: 'typesafe', builtin: true, caps: { probability: 'native' } },
        { name: 'claude', kind: 'anthropic', builtin: false, caps: { kinds: ['choice'], probability: 'sampled' } },
      ] })
      api.getOrgAutonomy.mockResolvedValue({ ...AUTONOMY, level, effective_level: level, decider: { ...AUTONOMY.decider, kind: 'jev', provider: 'claude', threshold } })
    }

    it('says it cannot reach the default 0.8 at level mid', async () => {
      sampled('mid', 0.8)
      await renderBar()
      expect(await screen.findByTestId('jev-decider-sampled')).toHaveTextContent(
        "This provider estimates from 5 answers: on the decider's 3 verdicts it reaches at most 0.75, so it cannot reach this threshold and the model decider decides.")
    })

    it('says 0.75 at level mid takes all five answers', async () => {
      sampled('mid', 0.75)
      await renderBar()
      expect(await screen.findByTestId('jev-decider-sampled')).toHaveTextContent(
        "on the decider's 3 verdicts it reaches 0.75 only when all 5 agree.")
    })

    it('says nothing at 0.8 on full (two verdicts, up to 0.86), and warns at 0.9', async () => {
      sampled('full', 0.8)
      await renderBar()
      await waitFor(() => expect(api.jevProviders).toHaveBeenCalled())
      await screen.findByLabelText('Provider')
      expect(screen.queryByTestId('jev-decider-sampled')).not.toBeInTheDocument()
      cleanup()
      sampled('full', 0.9)
      await renderBar()
      expect(await screen.findByTestId('jev-decider-sampled')).toHaveTextContent("on the decider's 2 verdicts it reaches at most 0.86")
    })
  })
})
