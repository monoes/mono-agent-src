// @vitest-environment jsdom
// Welcome: what will be set up, "Get started", and the welcome mark.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import i18n from '../../i18n.js'
import Welcome from './Welcome.jsx'

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  props = {
    mark: vi.fn(() => Promise.resolve({ ok: true })),
    setCanContinue: vi.fn(),
    setOnContinue: vi.fn(),
    setContinueLabel: vi.fn(),
  }
})
afterEach(cleanup)

const handler = () => props.setOnContinue.mock.calls.at(-1)[0]

describe('Welcome', () => {
  it('lists the five things that will be set up and marks welcome on Get started', async () => {
    render(<Welcome {...props} />)
    expect(screen.getByText(/run on your own machine/)).toBeInTheDocument()
    const items = screen.getAllByRole('listitem').map(li => li.textContent)
    expect(items).toEqual(['Your profile', 'The engine (monomind)', 'An AI agent', 'Your browser', 'A first run'])
    expect(props.setContinueLabel).toHaveBeenCalledWith('Get started')
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)

    let result
    await act(async () => { result = await handler()() })
    expect(props.mark).toHaveBeenCalledWith('welcome')
    expect(result).not.toBe(false)
  })

  it('stays on the step and shows the error when the mark fails', async () => {
    props.mark = vi.fn(() => Promise.resolve({ ok: false, loginRequired: false, message: 'disk full' }))
    render(<Welcome {...props} />)
    let result
    await act(async () => { result = await handler()() })
    expect(result).toBe(false)
    expect(screen.getByRole('alert')).toHaveTextContent('disk full')
  })
})
