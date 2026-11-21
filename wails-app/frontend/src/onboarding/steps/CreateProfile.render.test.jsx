// @vitest-environment jsdom
// Create your profile: name (with suggestions), icon, folder, preview, and
// CreateProfile with what was chosen; the Go error stays on the step.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'
import i18n from '../../i18n.js'

const go = vi.hoisted(() => ({ CreateProfile: vi.fn(), ChooseProfileFolder: vi.fn() }))
vi.mock('../../wailsjs/go/main/App', () => go)

import CreateProfile, { PROFILE_ICONS } from './CreateProfile.jsx'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (profileStatus = 'todo') => ({
  completed: false, currentStep: 'profile', profileId: profileStatus === 'done' ? 'p1' : '',
  steps: IDS.map(id => ({ id, status: id === 'profile' ? profileStatus : 'todo' })),
})

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  go.CreateProfile.mockReset()
  go.ChooseProfileFolder.mockReset()
  props = {
    state: stateOf(),
    reload: vi.fn(() => Promise.resolve({ ok: true })),
    setCanContinue: vi.fn(),
    setOnContinue: vi.fn(),
    setContinueLabel: vi.fn(),
  }
})
afterEach(cleanup)

const handler = () => props.setOnContinue.mock.calls.at(-1)[0]
const runContinue = async () => { let r; await act(async () => { r = await handler()() }); return r }
const nameField = () => screen.getByLabelText('Profile name')

describe('CreateProfile', () => {
  it('disables Create profile while the name is empty', () => {
    render(<CreateProfile {...props} />)
    expect(props.setContinueLabel).toHaveBeenLastCalledWith('Create profile')
    expect(props.setCanContinue).toHaveBeenLastCalledWith(false)
    fireEvent.change(nameField(), { target: { value: 'Work' } })
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
    fireEvent.change(nameField(), { target: { value: '   ' } })
    expect(props.setCanContinue).toHaveBeenLastCalledWith(false)
    expect(screen.getByText('Give your profile a name.')).toBeInTheDocument()
  })

  it('refuses the name "default" before calling Go', () => {
    render(<CreateProfile {...props} />)
    fireEvent.change(nameField(), { target: { value: ' Default ' } })
    expect(props.setCanContinue).toHaveBeenLastCalledWith(false)
    expect(screen.getByText('Pick a name that says what this profile is for.')).toBeInTheDocument()
    expect(nameField()).toHaveAttribute('aria-invalid', 'true')
  })

  it('shows the Go error under the field and stays on the step', async () => {
    go.CreateProfile.mockRejectedValue('a profile named "Work" already exists')
    render(<CreateProfile {...props} />)
    fireEvent.change(nameField(), { target: { value: 'Work' } })
    const r = await runContinue()
    expect(r).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('a profile named "Work" already exists')
    expect(nameField()).toHaveAttribute('aria-invalid', 'true')
    expect(props.reload).not.toHaveBeenCalled()
  })

  it('shows the default-name message when Go refuses the name as "default"', async () => {
    go.CreateProfile.mockRejectedValue('profile name cannot be "default": no profile is the default')
    render(<CreateProfile {...props} />)
    fireEvent.change(nameField(), { target: { value: 'défault' } })
    expect(await runContinue()).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('Pick a name that says what this profile is for.')
  })

  it('shows the sign-in message on a login-required answer', async () => {
    go.CreateProfile.mockRejectedValue('Log in to monoes.me first: run monoagentcli login')
    render(<CreateProfile {...props} />)
    fireEvent.change(nameField(), { target: { value: 'Work' } })
    expect(await runContinue()).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('Sign in to monoes.me to continue.')
  })

  it('creates with the chosen name, folder and icon, then reloads state', async () => {
    go.CreateProfile.mockResolvedValue({ id: 'p1', name: 'Work' })
    render(<CreateProfile {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Work' }))
    expect(nameField()).toHaveValue('Work')
    const second = PROFILE_ICONS[1]
    const radios = screen.getAllByRole('radio')
    expect(radios).toHaveLength(8)
    expect(radios[0]).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(radios[1])
    expect(radios[1]).toHaveAttribute('aria-checked', 'true')
    // The preview shows the profile as the sidebar will.
    expect(screen.getByTestId('profile-preview')).toHaveTextContent('Work')

    const r = await runContinue()
    expect(go.CreateProfile).toHaveBeenCalledWith('Work', '', second)
    expect(props.reload).toHaveBeenCalled()
    expect(r).not.toBe(false)
  })

  it('passes the folder picked with Change, and keeps it when the picker is cancelled', async () => {
    go.CreateProfile.mockResolvedValue({ id: 'p1', name: 'Client' })
    go.ChooseProfileFolder.mockResolvedValueOnce('/Users/me/Clients/Acme').mockResolvedValueOnce('')
    render(<CreateProfile {...props} />)
    fireEvent.change(nameField(), { target: { value: 'Acme' } })
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Change' })) })
    expect(screen.getByText('/Users/me/Clients/Acme')).toBeInTheDocument()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Change' })) })
    expect(screen.getByText('/Users/me/Clients/Acme')).toBeInTheDocument()
    await runContinue()
    expect(go.CreateProfile).toHaveBeenCalledWith('Acme', '/Users/me/Clients/Acme', PROFILE_ICONS[0])
  })

  it('lets Continue through without creating when a profile was already made', async () => {
    props.state = stateOf('done')
    render(<CreateProfile {...props} />)
    // No second profile here: CreateProfile selects only the first one.
    expect(screen.queryByLabelText('Profile name')).toBeNull()
    expect(screen.getByText(/already made your profile/)).toBeInTheDocument()
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
    expect(props.setContinueLabel).toHaveBeenLastCalledWith(null)
    expect(await runContinue()).not.toBe(false)
    expect(go.CreateProfile).not.toHaveBeenCalled()
  })
})
