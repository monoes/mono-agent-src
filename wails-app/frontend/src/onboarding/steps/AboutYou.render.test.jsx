// @vitest-environment jsdom
// About you: the fields become profile Info entries (only the filled ones),
// the chosen chips one about_me line, then the about mark.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'
import i18n from '../../i18n.js'

const go = vi.hoisted(() => ({ SetProfileInfo: vi.fn() }))
vi.mock('../../wailsjs/go/main/App', () => go)

import AboutYou from './AboutYou.jsx'

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  go.SetProfileInfo.mockReset()
  go.SetProfileInfo.mockResolvedValue(undefined)
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
const type = (label, value) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

describe('AboutYou', () => {
  it('says what the Info is used for and where to edit it', () => {
    render(<AboutYou {...props} />)
    expect(screen.getByText('Used when agents fill forms and applications for you.')).toBeInTheDocument()
    expect(screen.getByText('You can edit this later in the Info tab.')).toBeInTheDocument()
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
  })

  it('writes full_name, first_name, last_name, job_title, employer and about_me, skipping empty fields', async () => {
    render(<AboutYou {...props} />)
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
    render(<AboutYou {...props} />)
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
    render(<AboutYou {...props} />)
    type('Role', 'Founder')
    const r = await runContinue()
    expect(order).toEqual(['job_title', 'mark:about'])
    expect(r).not.toBe(false)
  })

  it('marks about done with nothing written when the form is empty', async () => {
    render(<AboutYou {...props} />)
    await runContinue()
    expect(go.SetProfileInfo).not.toHaveBeenCalled()
    expect(props.mark).toHaveBeenCalledWith('about')
  })

  it('stops at the first failed write, shows it and stays', async () => {
    go.SetProfileInfo.mockImplementation(async (k) => { if (k === 'first_name') throw 'first_name: too long' })
    render(<AboutYou {...props} />)
    type('Your name', 'Ada Lovelace')
    type('Role', 'Founder')
    expect(await runContinue()).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('first_name: too long')
    expect(go.SetProfileInfo.mock.calls.map(c => c[0])).toEqual(['full_name', 'first_name'])
    expect(props.mark).not.toHaveBeenCalled()
  })

  it('shows the sign-in message on a login-required answer', async () => {
    go.SetProfileInfo.mockRejectedValue('Log in to monoes.me first: run monoagentcli login')
    render(<AboutYou {...props} />)
    type('Role', 'Founder')
    expect(await runContinue()).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('Sign in to monoes.me to continue.')
  })
})
