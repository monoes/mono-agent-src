// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import i18n from '../../i18n.js'

// The coder pane's session section: the unattended switch is always shown with the
// access level, the other settings sit in a disclosure, and each change names only
// the setting it changes and shows what the CLI stored.
const { api } = vi.hoisted(() => ({ api: { getCoderSessionSettings: vi.fn(), setCoderSessionSettings: vi.fn() } }))
vi.mock('../../services/api.js', () => ({ api }))

import { SessionSettings } from './SessionSettings.jsx'

const view = (over = {}, settings = {}) => ({
  conversationId: 'c1', role: 'solo', unattended: true, access: 'coding', operatorOnly: [],
  allowed: {
    autonomy_preset: ['ask-always', 'ask-on-risk', 'decide-and-log'],
    gap_classes: ['ambiguity', 'risky_action'],
    mesh_accept: ['none', 'project', 'subtree'],
  },
  settings: {
    checkin_minutes: 15, skip_if_unchanged: true, autonomy_preset: 'ask-on-risk', autonomy_classes: {},
    max_children: 4, max_concurrent: 2, max_depth: 2, checkin_limit: 48, mesh_visible: true, mesh_alias: '', mesh_accept: 'project', ...settings,
  },
  ...over,
})

beforeEach(() => {
  vi.clearAllMocks()
  api.getCoderSessionSettings.mockResolvedValue(view())
})
afterEach(cleanup)

const shown = async () => { render(<SessionSettings conversationId="c1" access="coding" />); await screen.findByTestId('pane-unattended') }

describe('SessionSettings', () => {
  it('always shows the unattended state with the access level', async () => {
    await shown()
    const sw = screen.getByRole('switch')
    expect(sw).toHaveAttribute('aria-checked', 'true')
    expect(sw).toHaveTextContent('Unattended: on')
    expect(sw).toHaveTextContent(/coding/i)
    expect(screen.getByTestId('pane-unattended')).toBeVisible()
  })

  it('says when a master is waiting for a person at its check-in limit, and only then', async () => {
    api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 48, limit: 48, waiting: true } }))
    await shown()
    expect(screen.getByTestId('pane-checkin-waiting')).toHaveTextContent('Waiting for you: 48 check-in turns ran with no message from you')
    cleanup()
    api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 1, limit: 1, waiting: true } }))
    await shown()
    expect(screen.getByTestId('pane-checkin-waiting')).toHaveTextContent('Waiting for you: 1 check-in turn ran with')
    cleanup()
    api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 3, limit: 48, waiting: false } }))
    await shown()
    expect(screen.getByTestId('pane-checkin-waiting')).toBeEmptyDOMElement()
    cleanup()
    api.getCoderSessionSettings.mockResolvedValue(view()) // not a master: no check-in state at all
    await shown()
    expect(screen.getByTestId('pane-checkin-waiting')).toBeEmptyDOMElement()
  })

  it('keeps the note up to date while the pane is open: it appears by itself and clears when a person writes', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 47, limit: 48, waiting: false } }))
      await shown()
      expect(screen.getByTestId('pane-checkin-waiting')).toBeEmptyDOMElement()
      api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 48, limit: 48, waiting: true } }))
      await vi.advanceTimersByTimeAsync(15500)
      await waitFor(() => expect(screen.getByTestId('pane-checkin-waiting')).toHaveTextContent('Waiting for you: 48'))
      api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 0, limit: 48, waiting: false } }))
      await vi.advanceTimersByTimeAsync(15500)
      await waitFor(() => expect(screen.getByTestId('pane-checkin-waiting')).toBeEmptyDOMElement())
      api.getCoderSessionSettings.mockRejectedValue(new Error('boom')) // a refresh that fails keeps what is shown
      await vi.advanceTimersByTimeAsync(15500)
      expect(screen.getByTestId('pane-unattended')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('turns unattended off through the CLI and shows the stored answer', async () => {
    api.setCoderSessionSettings.mockResolvedValue(view({ unattended: false }))
    await shown()
    fireEvent.click(screen.getByRole('switch'))
    await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenCalledWith('c1', null, 'false'))
    await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'false'))
    expect(screen.getByRole('switch')).toHaveTextContent('Unattended: off')
  })

  it('changes the check-in interval by naming only that setting', async () => {
    api.setCoderSessionSettings.mockResolvedValue(view({}, { checkin_minutes: 30 }))
    await shown()
    fireEvent.change(screen.getByLabelText('Check in every'), { target: { value: '30' } })
    await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenCalledWith('c1', { checkin_minutes: 30 }, ''))
    await waitFor(() => expect(screen.getByLabelText('Check in every')).toHaveValue('30'))
  })

  it('keeps an interval the list does not offer (it was set on the command line)', async () => {
    api.getCoderSessionSettings.mockResolvedValue(view({}, { checkin_minutes: 7 }))
    await shown()
    expect(screen.getByLabelText('Check in every')).toHaveValue('7')
  })

  it('sets an autonomy override for one kind of gap and removes it again', async () => {
    api.setCoderSessionSettings.mockResolvedValueOnce(view({}, { autonomy_classes: { risky_action: { who: 'master', fallback: 'park' } } }))
    await shown()
    fireEvent.change(screen.getByLabelText('Risky action'), { target: { value: 'master' } })
    await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenLastCalledWith('c1', { autonomy_classes: { risky_action: { who: 'master', fallback: 'park' } } }, ''))
    api.setCoderSessionSettings.mockResolvedValueOnce(view())
    await waitFor(() => expect(screen.getByLabelText('Risky action')).toHaveValue('master'))
    fireEvent.change(screen.getByLabelText('Risky action'), { target: { value: '' } })
    await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenLastCalledWith('c1', { autonomy_classes: {} }, ''))
  })

  it('commits a limit when the field is left, and ignores a value that did not change', async () => {
    api.setCoderSessionSettings.mockResolvedValue(view({}, { max_children: 6 }))
    await shown()
    const field = screen.getByLabelText('Max child sessions')
    fireEvent.blur(field)
    expect(api.setCoderSessionSettings).not.toHaveBeenCalled()
    fireEvent.change(field, { target: { value: '6' } })
    fireEvent.blur(field)
    await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenCalledWith('c1', { max_children: 6 }, ''))
  })

  it('changes the number of check-ins in a row by naming only that setting', async () => {
    api.setCoderSessionSettings.mockResolvedValue(view({}, { checkin_limit: 12 }))
    await shown()
    const field = screen.getByLabelText('Check-ins in a row')
    expect(field).toHaveValue(48)
    fireEvent.change(field, { target: { value: '12' } })
    fireEvent.blur(field)
    await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenCalledWith('c1', { checkin_limit: 12 }, ''))
  })

  // The translation function changes identity when the language is set up or switched, which on a
  // slow machine can happen after the pane showed. That must not reload the settings and unmount the
  // section under the person's hands.
  it('keeps what is shown, and its fields, when the translation function changes', async () => {
    await shown()
    const field = screen.getByLabelText('Check-ins in a row')
    const reads = api.getCoderSessionSettings.mock.calls.length
    try {
      await act(async () => { await i18n.changeLanguage('es') })
      await act(async () => { await i18n.changeLanguage('en') })
    } finally {
      await act(async () => { await i18n.changeLanguage('en') })
    }
    expect(screen.getByLabelText('Check-ins in a row')).toBe(field)
    expect(api.getCoderSessionSettings.mock.calls.length).toBe(reads)
  })

  // A refresh that started before a save and answers after it must not put the old values back.
  it('does not let a slow refresh overwrite a save that finished after it started', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await shown()
      let release
      api.getCoderSessionSettings.mockImplementation(() => new Promise(resolve => { release = () => resolve(view()) })) // answers with the old 48
      await vi.advanceTimersByTimeAsync(15500) // the refresh starts and waits
      expect(release).toBeTypeOf('function')
      api.setCoderSessionSettings.mockResolvedValue(view({}, { checkin_limit: 12 }))
      const field = screen.getByLabelText('Check-ins in a row')
      fireEvent.change(field, { target: { value: '12' } })
      fireEvent.blur(field)
      await waitFor(() => expect(screen.getByLabelText('Check-ins in a row')).toHaveValue(12))
      await act(async () => { release() })
      expect(screen.getByLabelText('Check-ins in a row')).toHaveValue(12)
    } finally {
      vi.useRealTimers()
    }
  })

  // What the person is typing is theirs until they leave the field: a refresh that brings another stored
  // value must not write over it.
  it('keeps what is being typed when a refresh brings another stored value', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await shown()
      const field = screen.getByLabelText('Max child sessions')
      fireEvent.change(field, { target: { value: '9' } })
      // changed elsewhere meanwhile; the answer also carries a note, so we can tell it has been rendered
      api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 48, limit: 48, waiting: true } }, { max_children: 5 }))
      await vi.advanceTimersByTimeAsync(15500)
      await waitFor(() => expect(screen.getByTestId('pane-checkin-waiting')).toHaveTextContent('Waiting for you'))
      expect(screen.getByLabelText('Max child sessions')).toHaveValue(9)
      api.setCoderSessionSettings.mockResolvedValue(view({}, { max_children: 9 }))
      fireEvent.blur(screen.getByLabelText('Max child sessions'))
      await waitFor(() => expect(api.setCoderSessionSettings).toHaveBeenCalledWith('c1', { max_children: 9 }, ''))
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not save an emptied field as 0, and shows the stored value again', async () => {
    await shown()
    const field = screen.getByLabelText('Max child sessions')
    fireEvent.change(field, { target: { value: '' } })
    expect(screen.getByLabelText('Max child sessions')).toHaveValue(null)
    fireEvent.blur(screen.getByLabelText('Max child sessions'))
    expect(api.setCoderSessionSettings).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Max child sessions')).toHaveValue(4)
  })

  it('shows the stored value of a field again once the person leaves it unchanged, after a refresh changed it', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await shown()
      const field = screen.getByLabelText('Max child sessions')
      fireEvent.change(field, { target: { value: '4' } }) // typed the value it already has
      api.getCoderSessionSettings.mockResolvedValue(view({ role: 'master', checkin: { streak: 48, limit: 48, waiting: true } }, { max_children: 7 }))
      await vi.advanceTimersByTimeAsync(15500)
      await waitFor(() => expect(screen.getByTestId('pane-checkin-waiting')).toHaveTextContent('Waiting for you'))
      fireEvent.blur(screen.getByLabelText('Max child sessions'))
      expect(api.setCoderSessionSettings).not.toHaveBeenCalled()
      expect(screen.getByLabelText('Max child sessions')).toHaveValue(7)
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows why a change was refused and keeps what was stored', async () => {
    api.setCoderSessionSettings.mockRejectedValue(new Error('max_children must be between 0 and 20'))
    await shown()
    const field = screen.getByLabelText('Max child sessions')
    fireEvent.change(field, { target: { value: '99' } })
    fireEvent.blur(field)
    expect(await screen.findByRole('alert')).toHaveTextContent('max_children must be between 0 and 20')
    expect(screen.getByLabelText('Max child sessions')).toHaveValue(4)
  })

  it('shows nothing for a session it cannot read, but says so', async () => {
    api.getCoderSessionSettings.mockRejectedValue(new Error('boom'))
    render(<SessionSettings conversationId="c1" access="coding" />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not read the session settings.')
    expect(screen.queryByTestId('pane-session')).toBeNull()
  })
})
