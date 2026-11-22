// @vitest-environment jsdom
// You're set: the summary of the nine steps, "Finish this" back to a step, the
// catalogue opening its page, and "Open MonoAgent" completing the onboarding.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor, within, act } from '@testing-library/react'

import i18n from '../../i18n.js'
import Finish from './Finish.jsx'
import Onboarding from '../Onboarding.jsx'
import { CATALOGUE } from '../catalogue.js'
import { PAGE_IDS } from '../../App.jsx'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (statuses = {}) => ({ completed: false, currentStep: 'finish', profileId: 'p1', steps: IDS.map(id => ({ id, status: statuses[id] || 'done' })) })

let props
function renderStep(state = stateOf(), over = {}) {
  props = {
    state, profileId: 'p1', mode: 'full', reload: vi.fn(), mark: vi.fn(), skip: vi.fn(),
    complete: vi.fn(() => Promise.resolve({ ok: true, value: undefined })),
    onDone: vi.fn(), onFinish: vi.fn(), goTo: vi.fn(),
    setCanContinue: vi.fn(), setOnContinue: vi.fn(), setContinueLabel: vi.fn(),
    ...over,
  }
  return render(<div className="onb-root"><Finish {...props} /></div>)
}
const summary = () => screen.getByRole('list', { name: 'Setup summary' })

beforeEach(async () => { await i18n.changeLanguage('en') })
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe("You're set", () => {
  it('summary shows each step as done or skipped', () => {
    renderStep(stateOf({ agents: 'skipped', firstRun: 'todo' }))
    const rows = within(summary()).getAllByRole('listitem')
    expect(rows).toHaveLength(9) // every step but this one
    const row = name => rows.find(r => r.textContent.startsWith(name))
    expect(row('Your agents')).toHaveTextContent('Skipped')
    expect(row('First run')).toHaveTextContent('Not done')
    expect(row('System check')).toHaveTextContent('done')
    expect(within(row('System check')).queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByText('2 steps are left for later. Everything else is ready.')).toBeInTheDocument()
  })

  it('says so when every step is done', () => {
    renderStep()
    expect(screen.getByText('Every step is done. Your village is ready.')).toBeInTheDocument()
    expect(within(summary()).queryAllByRole('button')).toHaveLength(0)
  })

  it('Finish this goes to the skipped step', () => {
    renderStep(stateOf({ browser: 'skipped' }))
    fireEvent.click(screen.getByRole('button', { name: 'Finish this: Browser' }))
    expect(props.goTo).toHaveBeenCalledWith('browser')
    expect(props.complete).not.toHaveBeenCalled()
  })

  it('a catalogue card completes onboarding and opens its page', async () => {
    renderStep()
    fireEvent.click(screen.getByRole('button', { name: /^Orgs/ }))
    await waitFor(() => expect(props.onFinish).toHaveBeenCalledWith(expect.objectContaining({ page: 'orgs' })))
    expect(props.complete).toHaveBeenCalledTimes(1)
    expect(props.complete.mock.invocationCallOrder[0]).toBeLessThan(props.onFinish.mock.invocationCallOrder[0])
  })

  it('a card passes its page options', async () => {
    renderStep()
    fireEvent.click(screen.getByRole('button', { name: /^Library/ }))
    await waitFor(() => expect(props.onFinish).toHaveBeenCalledWith({ page: 'noderunner', opts: { openTemplates: true } }))
  })

  it('the API card opens the API section of Settings', async () => {
    renderStep()
    fireEvent.click(screen.getByRole('button', { name: /^API and MCP/ }))
    await waitFor(() => expect(props.onFinish).toHaveBeenCalledWith({ page: 'settings', opts: { section: 'api' } }))
  })

  it('the cards wait while Continue completes', async () => {
    let done
    renderStep(stateOf(), { complete: vi.fn(() => new Promise(r => { done = r })) })
    const handler = props.setOnContinue.mock.calls.at(-1)[0]
    let pending
    act(() => { pending = handler() })
    expect(screen.getByRole('button', { name: /^Orgs/ })).toBeDisabled()
    await act(async () => { done({ ok: true }); await pending })
    expect(props.complete).toHaveBeenCalledTimes(1)
  })

  it('a card stays when completing fails, and says why', async () => {
    renderStep(stateOf(), { complete: vi.fn(() => Promise.resolve({ ok: false, loginRequired: true, message: 'Log in to monoes.me first.' })) })
    fireEvent.click(screen.getByRole('button', { name: /^Tasks/ }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(i18n.t('onboarding.shell.signIn')))
    expect(props.onFinish).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /^Tasks/ })).toBeEnabled()
  })

  it('every catalogue page exists in the app', () => {
    expect(CATALOGUE).toHaveLength(11)
    for (const card of CATALOGUE) expect(PAGE_IDS).toContain(card.page)
  })

  it('Open MonoAgent completes and opens the app', async () => {
    const complete = vi.fn(() => Promise.resolve({ ok: true, value: undefined }))
    const onFinish = vi.fn()
    render(<Onboarding state={stateOf()} startStep="finish" onFinish={onFinish} complete={complete}
      reload={vi.fn(() => Promise.resolve({ ok: true }))} mark={vi.fn()} skip={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open MonoAgent' }))
    await waitFor(() => expect(onFinish).toHaveBeenCalledTimes(1))
    expect(complete).toHaveBeenCalledTimes(1)
    expect(onFinish.mock.calls[0][0]).toBeUndefined() // the app's own start page
  })

  it('Open MonoAgent stays when completing fails', async () => {
    const complete = vi.fn(() => Promise.resolve({ ok: false, loginRequired: false, message: 'could not write the state' }))
    const onFinish = vi.fn()
    render(<Onboarding state={stateOf()} startStep="finish" onFinish={onFinish} complete={complete}
      reload={vi.fn(() => Promise.resolve({ ok: true }))} mark={vi.fn()} skip={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open MonoAgent' }))
    await waitFor(() => expect(screen.getByText('could not write the state')).toBeInTheDocument())
    expect(onFinish).not.toHaveBeenCalled()
  })
})
