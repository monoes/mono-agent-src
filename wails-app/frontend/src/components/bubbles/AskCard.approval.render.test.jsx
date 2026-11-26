// @vitest-environment jsdom
// The chat bubble's approval card shows the whole call Approve releases, and
// offers Approve only for a call it can name by its request id.
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, act } from '@testing-library/react'
import i18n from '../../i18n.js'
import en from '../../locales/en.json'
import es from '../../locales/es.json'
import { AskCard } from './AskCard.jsx'
import { OrgChatView } from './OrgChatView.jsx'

vi.mock('./useHilAsks.js', () => ({ useHilAsks: () => ({ asks: [], busy: {}, outcome: {} }) }))
vi.mock('../../services/api.js', () => ({ api: {}, notify: vi.fn(), onOrgEvent: () => () => {} }))

afterEach(async () => {
  cleanup()
  await act(() => i18n.changeLanguage('en'))
})

const named = { id: 'e1', ref: 'apr-1', request_id: 'apr-1', kind: 'approval', role: 'dev', action: 'Bash', text: 'Approval required for Bash', pending: true }
const unnamed = { ...named, ref: 'dev:Bash:5', request_id: undefined }

describe('the bubble approval card shows the whole call', () => {
  const input = { command: 'git push', dangerouslyDisableSandbox: true, description: 'safe‮evil', run_in_background: true }

  it('shows the command and every other argument beside Approve, invisible characters marked', () => {
    render(<AskCard ask={{ ...named, command: 'git push', cwd: '/w', input }} />)
    expect(screen.getByTestId('approval-command')).toHaveTextContent('git push')
    const others = screen.getByTestId('approval-input')
    expect(others).toHaveTextContent('"dangerouslyDisableSandbox": true')
    expect(others).toHaveTextContent('"run_in_background": true')
    expect(others).not.toHaveTextContent('"command"')
    expect(others.textContent).not.toContain('‮')
    expect(screen.getByTestId('approval-escape')).toHaveTextContent('\\u202E')
    expect(screen.getByTestId('org-chat-approve')).toBeEnabled()
  })

  it('shows the arguments of a call that has no command', () => {
    render(<AskCard ask={{ ...named, action: 'WebFetch', input: { url: 'https://example.com/x', prompt: 'p' } }} />)
    expect(screen.getByTestId('approval-input')).toHaveTextContent('https://example.com/x')
    expect(screen.queryByTestId('approval-command')).toBeNull()
  })

  it.each([['en', en], ['es', es]])('warns about cut, hidden and unseen arguments (%s)', async (lang, words) => {
    await act(() => i18n.changeLanguage(lang))
    const long = Object.fromEntries(Array.from({ length: 20 }, (_, i) => [`k${i}`, i]))
    render(<AskCard ask={{ ...named, input: { ...long, content: `${'a'.repeat(200)}…`, token: '[REDACTED]' } }} />)
    for (const key of ['inputMore', 'inputCut', 'inputRedacted']) expect(screen.getByText(words.ask[key])).toBeInTheDocument()
    expect(screen.getByLabelText(words.ask.inputLabel)).toBeInTheDocument()
  })
})

describe('an approval with no request id', () => {
  it.each([['en', en], ['es', es]])('offers no Approve, says why and how to go on, and still denies (%s)', async (lang, words) => {
    await act(() => i18n.changeLanguage(lang))
    const onDecide = vi.fn()
    render(<AskCard ask={{ ...unnamed, command: 'ls' }} org="growth" onDecide={onDecide} />)
    expect(screen.queryByTestId('org-chat-approve')).toBeNull()
    const note = screen.getByRole('note')
    expect(note).toHaveTextContent(words.ui7Hil.noid_approve_before)
    expect(note).toHaveTextContent('monoagentcli --profile <profile> org approve growth dev Bash')
    expect(note).not.toHaveTextContent('approve|deny')
    expect(screen.getByTestId('approval-command')).toHaveTextContent('ls')
    fireEvent.click(screen.getByTestId('org-chat-deny'))
    expect(onDecide).toHaveBeenCalledWith(expect.objectContaining({ ref: 'dev:Bash:5' }), false, { note: '' })
  })

  it('keeps odd names out of the command to paste', () => {
    render(<AskCard ask={{ ...unnamed, role: 'dev; echo pasted', action: 'Ba‮sh' }} org="growth" />)
    expect(screen.getByRole('note')).toHaveTextContent('org approve growth <role> <action>')
  })

  it('says nothing once resolved, and leaves a gate as it was', () => {
    const { rerender } = render(<AskCard ask={{ ...unnamed, pending: false, resolution: 'denied' }} org="growth" />)
    expect(screen.queryByRole('note')).toBeNull()
    rerender(<AskCard ask={{ id: 'g', ref: 'gate-1', kind: 'gate', role: 'ceo', name: 'ship', pending: true }} org="growth" />)
    expect(screen.getByTestId('org-chat-approve')).toBeEnabled()
    expect(screen.queryByRole('note')).toBeNull()
  })

  it('names the org of the bubble it is shown in', () => {
    const conv = { history: { roles: [{ id: 'dev' }] }, items: [unnamed], echoes: [], running: true, busy: {}, outcome: {}, bubble: null }
    render(<OrgChatView conv={conv} nameOf={id => id} orgName="acme" draft="" onDraftChange={() => {}} />)
    expect(screen.getByRole('note')).toHaveTextContent('org approve acme dev Bash')
  })

  it('has its words in both locales', () => {
    for (const key of ['noid_approve_before', 'noid_approve_after']) {
      expect(en.ui7Hil[key]).toBeTruthy()
      expect(es.ui7Hil[key]).toBeTruthy()
      expect(es.ui7Hil[key]).not.toBe(en.ui7Hil[key])
    }
  })
})
