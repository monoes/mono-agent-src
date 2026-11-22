// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import '../../i18n.js'
import ConfirmHost from '../ConfirmDialog.jsx'
import CoderOrgSection from './CoderOrgSection.jsx'

// Each control calls its binding (and so `monoagentcli coder set` / `hil
// allow …`) with the right arguments.

const App = {}
beforeEach(() => {
  for (const k of ['CoderStatus', 'CoderSet', 'HilSendSetApproval', 'CoderSetAppAccess', 'HilSendAllowList', 'HilSendAllowAdd', 'HilSendAllowRemove']) App[k] = vi.fn()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const status = (over = {}) => JSON.stringify({ enabled: true, orgRouter: 'auto', sendApproval: true, appAccess: true, ...over })
const rules = [
  { id: 'r1', action: 'email', platform: 'outlook', account: 'me@acme.test', recipient: 'sam@acme.test' },
  { id: 'r2', action: 'dm', platform: 'linkedin', account: '', recipient: 'https://www.linkedin.com/in/sam-rivera' },
]

function machine(st = status()) {
  App.CoderStatus.mockResolvedValue(st)
  return render(<><CoderOrgSection part="machine" /><ConfirmHost /></>)
}

describe('CoderOrgSection, this computer', () => {
  it('sets the org router with `coder set --org-router`', async () => {
    machine()
    const router = await screen.findByRole('combobox', { name: "Who picks a worker's role" })
    expect(router).toHaveValue('auto')
    expect(within(router).getAllByRole('option').map(o => o.value)).toEqual(['auto', 'jev', 'model'])
    App.CoderSet.mockResolvedValue(status({ orgRouter: 'model' }))
    fireEvent.change(router, { target: { value: 'model' } })
    // Only the router changes: the other settings are passed as "leave alone".
    await waitFor(() => expect(App.CoderSet).toHaveBeenCalledWith('', 0, '', -1, 'model'))
    await waitFor(() => expect(router).toHaveValue('model'))
    expect(screen.getByText(/The lead names roles from its org roster/)).toBeInTheDocument()
  })

  it('turning send approval off asks first; cancelling changes nothing', async () => {
    machine()
    const box = await screen.findByRole('checkbox', { name: "Agents' sends wait for my approval" })
    expect(box).toBeChecked()
    fireEvent.click(box)
    const dialog = await screen.findByRole('dialog', { name: 'Let agents send without asking?' })
    expect(dialog).toHaveTextContent('Agents could send email, direct messages, comments and posts, and publish, as you, without asking you first.')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(App.HilSendSetApproval).not.toHaveBeenCalled()
    expect(box).toBeChecked()
  })

  it('confirming turns send approval off; turning it on needs no confirmation', async () => {
    machine()
    const box = await screen.findByRole('checkbox', { name: "Agents' sends wait for my approval" })
    App.HilSendSetApproval.mockResolvedValue(status({ sendApproval: false }))
    fireEvent.click(box)
    fireEvent.click(await screen.findByRole('button', { name: 'Let agents send' }))
    await waitFor(() => expect(App.HilSendSetApproval).toHaveBeenCalledWith(false))
    await waitFor(() => expect(box).not.toBeChecked())
    App.HilSendSetApproval.mockResolvedValue(status({ sendApproval: true }))
    fireEvent.click(box)
    await waitFor(() => expect(App.HilSendSetApproval).toHaveBeenLastCalledWith(true))
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() => expect(box).toBeChecked())
  })

  // The CLI's wording since #870: the switch changes what agents are told
  // and offered, not what they can do.
  it("turns what coder agents are told about MonoAgent off and on with `coder set --app-access`", async () => {
    machine()
    const box = await screen.findByRole('checkbox', { name: 'Coder agents are told about MonoAgent' })
    expect(box).toBeChecked()
    const help = screen.getByText(/Off: org workers aren't told about or offered MonoAgent's tabs, and agents get no profile summary;/)
    expect(help).toHaveTextContent("the coder lead keeps full access, operator-only commands and send approval still apply.")
    expect(help).not.toHaveTextContent(/Jev/)
    App.CoderSetAppAccess.mockResolvedValue(status({ appAccess: false }))
    fireEvent.click(box)
    await waitFor(() => expect(App.CoderSetAppAccess).toHaveBeenCalledWith(false))
    await waitFor(() => expect(box).not.toBeChecked())
    App.CoderSetAppAccess.mockResolvedValue(status({ appAccess: true }))
    fireEvent.click(box)
    await waitFor(() => expect(App.CoderSetAppAccess).toHaveBeenLastCalledWith(true))
    await waitFor(() => expect(box).toBeChecked())
    expect(App.HilSendSetApproval).not.toHaveBeenCalled()
  })

  it('has no app access control when the CLI reports none (an older monoagentcli)', async () => {
    machine(JSON.stringify({ enabled: true, orgRouter: 'auto', sendApproval: true }))
    await screen.findByRole('checkbox', { name: "Agents' sends wait for my approval" })
    expect(screen.queryByRole('checkbox', { name: 'Coder agents are told about MonoAgent' })).toBeNull()
  })

  it('says on the router picker which choices send the brief to Jev', async () => {
    machine()
    const router = await screen.findByRole('combobox', { name: "Who picks a worker's role" })
    // Security review of #871: whenever a decision provider resolves, it gets
    // the brief under every router (the model and effort questions), so
    // nothing may say a choice here keeps it local. The sentence follows
    // `coder set --help`: briefs stay only when no Jev key or provider
    // resolves, or that provider is on this computer.
    const note = screen.getByText(/^When a decision provider resolves \(the dynorg feature's, else the machine default: TypeSafe with a Jev key set, or a provider you added\), a worker's brief goes to it whatever you pick here/)
    expect(note).toHaveTextContent("Briefs stay on this machine only when no Jev key or provider resolves, or when that provider's host is this computer (and even then the local server may forward them).")
    expect(note).not.toHaveTextContent(/The lead keeps|keeps it on this machine|Only removing the Jev key/)
    expect(router.parentElement).toContainElement(note)
  })

  it('shows the CLI refusal', async () => {
    machine()
    const router = await screen.findByRole('combobox', { name: "Who picks a worker's role" })
    // --org-router is operator-only (router=jev sends briefs to TypeSafe):
    // a refusal shows the CLI's text and the router stays as it was.
    App.CoderSet.mockResolvedValue(JSON.stringify({ error: 'only the operator may change coder mode', code: 'operator_only' }))
    fireEvent.change(router, { target: { value: 'jev' } })
    expect(await screen.findByRole('alert')).toHaveTextContent('only the operator may change coder mode')
    await waitFor(() => expect(router).not.toBeDisabled())
    expect(router).toHaveValue('auto')
  })
})

describe('CoderOrgSection, the profile', () => {
  it('lists the always-allow rules, adds one and removes one', async () => {
    App.HilSendAllowList.mockResolvedValue(JSON.stringify(rules))
    App.HilSendAllowAdd.mockResolvedValue(JSON.stringify({ action: 'comment', platform: 'x', account: '@me', recipient: '@sam' }))
    App.HilSendAllowRemove.mockResolvedValue(JSON.stringify({ removed: 'r1' }))
    render(<CoderOrgSection part="profile" />)
    const first = await screen.findByText('sam@acme.test')
    expect(first.closest('[data-rule]')).toHaveTextContent('Email · outlook · me@acme.test')
    expect(document.querySelector('[data-rule="r2"]')).toHaveTextContent('Direct message · linkedin · —')

    fireEvent.change(screen.getByRole('combobox', { name: 'Action' }), { target: { value: 'comment' } })
    fireEvent.change(screen.getByRole('textbox', { name: 'Platform' }), { target: { value: ' x ' } })
    fireEvent.change(screen.getByRole('textbox', { name: 'From account' }), { target: { value: '@me' } })
    fireEvent.change(screen.getByRole('textbox', { name: 'To' }), { target: { value: '@sam' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(App.HilSendAllowAdd).toHaveBeenCalledWith('comment', 'x', '@me', '@sam'))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'To' })).toHaveValue(''))

    fireEvent.click(screen.getByRole('button', { name: 'Stop always allowing sam@acme.test' }))
    await waitFor(() => expect(App.HilSendAllowRemove).toHaveBeenCalledWith('r1'))
    expect(App.HilSendAllowList).toHaveBeenCalledTimes(3)
  })

  // #871 review: a rule whose recipient reads like a label ("the ticket's
  // requester") matches no send; the form refuses it and says why.
  it('refuses a recipient that reads like a description, not an address', async () => {
    App.HilSendAllowList.mockResolvedValue('[]')
    render(<CoderOrgSection part="profile" />)
    const to = await screen.findByRole('textbox', { name: 'To' })
    const add = screen.getByRole('button', { name: 'Add' })
    for (const v of ["the ticket's requester", '  The customer  ', '\u200bthe customer']) {
      fireEvent.change(to, { target: { value: v } })
      expect(add).toBeDisabled()
      expect(screen.getByText(/reads like a description/)).toBeInTheDocument()
      expect(to).toHaveAttribute('aria-invalid', 'true')
      fireEvent.submit(to.closest('form'))
    }
    expect(App.HilSendAllowAdd).not.toHaveBeenCalled()
    for (const v of ['theo@acme.test', 'the-sam', 'the 2nd account', 'https://x.com/the_sam', 'Sam Rivera', 'the@acme.test']) {
      fireEvent.change(to, { target: { value: v } })
      expect(add).not.toBeDisabled()
      expect(screen.queryByText(/reads like a description/)).toBeNull()
    }
  })

  it('says when nothing is always allowed, and needs a recipient to add', async () => {
    App.HilSendAllowList.mockResolvedValue('[]')
    render(<CoderOrgSection part="profile" />)
    expect(await screen.findByText('No destination is always allowed.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add' })).toBeDisabled()
  })
})
