// @vitest-environment jsdom
// About you: the fields become profile Info entries (only the filled ones),
// the chosen chips one about_me line, then the about mark.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'
import i18n from '../../i18n.js'

const go = vi.hoisted(() => ({ SetProfileInfo: vi.fn(), GetProfileInfo: vi.fn() }))
vi.mock('../../wailsjs/go/main/App', () => go)

import AboutYou from './AboutYou.jsx'

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  go.SetProfileInfo.mockReset()
  go.SetProfileInfo.mockResolvedValue(undefined)
  go.GetProfileInfo.mockReset()
  go.GetProfileInfo.mockResolvedValue({ profile: 'p1', entries: [] })
  props = {
    mark: vi.fn(() => Promise.resolve({ ok: true })),
    setCanContinue: vi.fn(),
    setOnContinue: vi.fn(),
    setContinueLabel: vi.fn(),
  }
})
afterEach(cleanup)

const handler = () => props.setOnContinue.mock.calls.at(-1)[0]
const runContinue = async () => { let r; await act(async () => { r = await handler()() }); return r }
const show = async () => { const r = render(<AboutYou {...props} />); await act(async () => { await Promise.resolve() }); return r }
const info = (values) => ({ profile: 'p1', entries: Object.entries(values).map(([key, value]) => ({ key, value, masked: false })) })
const type = (label, value) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

describe('AboutYou', () => {
  it('says what the Info is used for and where to edit it', async () => {
    await show()
    expect(screen.getByText('Used when agents fill forms and applications for you.')).toBeInTheDocument()
    expect(screen.getByText('You can edit this later in the Info tab.')).toBeInTheDocument()
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
  })

  it('writes full_name, first_name, last_name, job_title, employer and about_me, skipping empty fields', async () => {
    await show()
    type('Your name', '  Ada King Lovelace ')
    type('Role', 'Founder')
    type('Company or project', '   ')
    fireEvent.click(screen.getByRole('button', { name: 'Research' }))
    fireEvent.click(screen.getByRole('button', { name: 'Coding' }))
    fireEvent.click(screen.getByRole('button', { name: 'Outreach' }))
    fireEvent.click(screen.getByRole('button', { name: 'Coding' })) // and off again
    expect(screen.getByRole('button', { name: 'Coding' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByRole('button', { name: 'Outreach' })).toHaveAttribute('aria-pressed', 'true')

    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([
      ['full_name', 'Ada King Lovelace'],
      ['first_name', 'Ada King'],
      ['last_name', 'Lovelace'],
      ['job_title', 'Founder'],
      ['about_me', 'Wants help with: Outreach, Research.'],
    ])
  })

  it('writes the company as employer and a one-word name as first_name only', async () => {
    await show()
    type('Your name', 'Ada')
    type('Company or project', 'Analytical Engines')
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([
      ['full_name', 'Ada'],
      ['first_name', 'Ada'],
      ['employer', 'Analytical Engines'],
    ])
  })

  it('marks about done after saving', async () => {
    const order = []
    go.SetProfileInfo.mockImplementation(async (k) => { order.push(k) })
    props.mark = vi.fn(async (id) => { order.push(`mark:${id}`); return { ok: true } })
    await show()
    type('Role', 'Founder')
    const r = await runContinue()
    expect(order).toEqual(['job_title', 'mark:about'])
    expect(r).not.toBe(false)
  })

  it('marks about done with nothing written when the form is empty', async () => {
    await show()
    await runContinue()
    expect(go.SetProfileInfo).not.toHaveBeenCalled()
    expect(props.mark).toHaveBeenCalledWith('about')
  })

  it('stops at the first failed write, shows it and stays', async () => {
    go.SetProfileInfo.mockImplementation(async (k) => { if (k === 'first_name') throw 'first_name: too long' })
    await show()
    type('Your name', 'Ada Lovelace')
    type('Role', 'Founder')
    expect(await runContinue()).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('first_name: too long')
    expect(go.SetProfileInfo.mock.calls.map(c => c[0])).toEqual(['full_name', 'first_name'])
    expect(props.mark).not.toHaveBeenCalled()
  })

  it('shows the sign-in message on a login-required answer', async () => {
    go.SetProfileInfo.mockRejectedValue('Log in to monoes.me first: run monoagentcli login')
    await show()
    type('Role', 'Founder')
    expect(await runContinue()).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('Your sign-in has ended. Sign in to monoes.me to continue.')
  })

  // A re-run on a profile that already has Info.
  it('prefills from the current Info and writes only what changed', async () => {
    go.GetProfileInfo.mockResolvedValue(info({
      full_name: 'Ada Lovelace', first_name: 'Ada', last_name: 'Lovelace', job_title: 'Founder', employer: 'Analytical Engines',
      about_me: 'I like maths.\nWants help with: Research.\nAnd poetry.',
    }))
    await show()
    expect(screen.getByLabelText('Your name')).toHaveValue('Ada Lovelace')
    expect(screen.getByLabelText('Role')).toHaveValue('Founder')
    expect(screen.getByLabelText('Company or project')).toHaveValue('Analytical Engines')
    expect(screen.getByRole('button', { name: 'Research' })).toHaveAttribute('aria-pressed', 'true')
    type('Role', 'CEO')
    fireEvent.click(screen.getByRole('button', { name: 'Outreach' }))
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([
      ['job_title', 'CEO'],
      // only our old line is replaced; the person's own text stays where it was
      ['about_me', 'I like maths.\nWants help with: Outreach, Research.\nAnd poetry.'],
    ])
    expect(props.mark).toHaveBeenCalledWith('about')
  })

  it('writes nothing when the prefilled values are left as they are', async () => {
    go.GetProfileInfo.mockResolvedValue(info({ full_name: 'Ada Lovelace', about_me: 'Wants help with: Inbox.' }))
    await show()
    expect(screen.getByRole('button', { name: 'Inbox' })).toHaveAttribute('aria-pressed', 'true')
    await runContinue()
    expect(go.SetProfileInfo).not.toHaveBeenCalled()
    expect(props.mark).toHaveBeenCalledWith('about')
  })

  it('adds our line after text of the person own, and removes only our line when every chip is off', async () => {
    go.GetProfileInfo.mockResolvedValue(info({ about_me: 'I like maths.' }))
    await show()
    fireEvent.click(screen.getByRole('button', { name: 'Coding' }))
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([['about_me', 'I like maths.\nWants help with: Coding.']])

    cleanup()
    go.SetProfileInfo.mockClear()
    go.GetProfileInfo.mockResolvedValue(info({ about_me: 'I like maths.\nWants help with: Coding.' }))
    await show()
    fireEvent.click(screen.getByRole('button', { name: 'Coding' }))
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([['about_me', 'I like maths.']])
  })

  it('clears about_me when it held only our line and every chip is turned off', async () => {
    go.GetProfileInfo.mockResolvedValue(info({ about_me: 'Wants help with: Coding.' }))
    await show()
    fireEvent.click(screen.getByRole('button', { name: 'Coding' }))
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([['about_me', '']])
  })

  it('recognises a line written in the other language and replaces it', async () => {
    go.GetProfileInfo.mockResolvedValue(info({ about_me: 'Quiere ayuda con: Investigación, Programación.' }))
    await show()
    expect(screen.getByRole('button', { name: 'Research' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Coding' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Coding' }))
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([['about_me', 'Wants help with: Research.']])
  })

  it('a changed name writes all three name fields', async () => {
    go.GetProfileInfo.mockResolvedValue(info({ first_name: 'Ada', last_name: 'Byron' }))
    await show()
    expect(screen.getByLabelText('Your name')).toHaveValue('Ada Byron')
    type('Your name', 'Ada Lovelace')
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([
      ['full_name', 'Ada Lovelace'], ['first_name', 'Ada'], ['last_name', 'Lovelace'],
    ])
  })

  it('renaming to a single word clears the old last_name', async () => {
    go.GetProfileInfo.mockResolvedValue(info({ full_name: 'Ada Lovelace', first_name: 'Ada', last_name: 'Lovelace' }))
    await show()
    type('Your name', 'Ada')
    await runContinue()
    expect(go.SetProfileInfo.mock.calls).toEqual([
      ['full_name', 'Ada'], ['first_name', 'Ada'], ['last_name', ''],
    ])
  })

  it('a failed Info read shows why, keeps Continue off, and can be tried again', async () => {
    go.GetProfileInfo.mockRejectedValueOnce('Log in to monoes.me first: run monoagentcli login')
      .mockResolvedValueOnce(info({ job_title: 'Founder' }))
    await show()
    expect(screen.getByRole('alert')).toHaveTextContent('Your sign-in has ended. Sign in to monoes.me to continue.')
    expect(props.setCanContinue).toHaveBeenLastCalledWith(false)
    expect(await runContinue()).toBe(false)
    expect(go.SetProfileInfo).not.toHaveBeenCalled()
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Try again' })) })
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByLabelText('Role')).toHaveValue('Founder')
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
  })
})
