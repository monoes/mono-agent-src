// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import '../../i18n.js'
import { AskItemCard, PersonCard } from './AskItemCard.jsx'
import { PersonItemsProvider } from './PersonItemsContext.jsx'
import { PersonItemsInbox, PersonItemsRegion, PersonItemTray } from './PersonItemViews.jsx'
import { OrbitRing } from './OrbitRing.jsx'
import { orbitOf } from '../../lib/sessionOrbit.js'
import { withWaiting } from '../../lib/personItems.js'

afterEach(() => { cleanup(); localStorage.clear() })

const ask = (extra = {}) => ({
  id: 8, kind: 'ancestor_question', askId: 4, sessionId: 31, conversationId: 'c-leaf', leadConversationId: 'c-leaf',
  askerTitle: 'Logger migration', ancestorTitle: 'The mission', question: 'May I drop the legacy table?', importance: 'critical', level: 'root', ...extra,
})

describe('AskItemCard', () => {
  it('says a lead asks for the person, who asked whom, and shows the question', () => {
    render(<AskItemCard item={ask()} onAnswer={vi.fn()} />)
    const card = screen.getByRole('group', { name: /A lead asks for you: Logger migration/ })
    expect(card).toHaveTextContent('A lead asks for you')
    expect(screen.getByTestId('ask-parties')).toHaveTextContent('Logger migration asks The mission')
    expect(screen.getByTestId('ask-question')).toHaveTextContent('May I drop the legacy table?')
    expect(screen.getByRole('textbox')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Answer' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: /Approve|Deny|Ask more/ })).toBeNull()
  })

  it('renders the question and titles as text, never as markup', () => {
    render(<AskItemCard item={ask({ question: '<img src=x onerror=alert(1)><b>do it</b>', askerTitle: '<script>x</script>' })} onAnswer={vi.fn()} />)
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('script')).toBeNull()
    expect(document.querySelector('.pi-question b')).toBeNull()
    expect(screen.getByTestId('ask-question').textContent).toBe('<img src=x onerror=alert(1)><b>do it</b>')
  })

  it('answers with the trimmed words through answerAsk', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ status: 'answered' })
    render(<AskItemCard item={ask()} onAnswer={onAnswer} />)
    fireEvent.change(screen.getByRole('textbox'), { target: { value: '  yes, drop it  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    await waitFor(() => expect(onAnswer).toHaveBeenCalledWith('answerAsk', expect.objectContaining({ askId: 4 }), 'yes, drop it'))
  })

  it('sends with ctrl+Enter, and never with empty words', async () => {
    const onAnswer = vi.fn().mockResolvedValue({})
    render(<AskItemCard item={ask()} onAnswer={onAnswer} />)
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ctrlKey: true })
    expect(onAnswer).not.toHaveBeenCalled()
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'ok' } })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(1))
  })

  it('shows a refusal where it was asked and keeps what was typed', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ error: 'that is not an open critical ask' })
    render(<AskItemCard item={ask()} onAnswer={onAnswer} />)
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'maybe' } })
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('not an open critical ask')
    expect(screen.getByRole('textbox')).toHaveValue('maybe')
  })

  it('PersonCard picks the ask card for an ancestor_question and the plan card otherwise', () => {
    render(<PersonCard item={ask()} onAnswer={vi.fn()} />)
    expect(screen.getByTestId('person-item-card')).toHaveAttribute('data-kind', 'ancestor_question')
    cleanup()
    render(<PersonCard item={{ id: 1, kind: 'plan_approval', sessionId: 7, title: 'P', reasons: [] }} onAnswer={vi.fn()} />)
    expect(screen.getByTestId('person-item-card')).toHaveAttribute('data-kind', 'plan_approval')
  })
})

const wrap = (items, extra = {}) => ({ items, loaded: true, refresh: vi.fn(), answer: vi.fn().mockResolvedValue({}), openAgent: vi.fn(), ...extra })
const renderIn = (value, ui) => render(<PersonItemsProvider value={value}>{ui}</PersonItemsProvider>)

describe('an ask item where the plan items show', () => {
  it('is in the asking agent pane, answers through the shared answer(), and is absent from other panes', () => {
    const v = wrap([ask()])
    renderIn(v, <PersonItemsRegion conversationId="c-leaf" />)
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'yes' } })
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    expect(v.answer).toHaveBeenCalledWith('answerAsk', expect.objectContaining({ askId: 4 }), 'yes')
    cleanup()
    expect(renderIn(wrap([ask()]), <PersonItemsRegion conversationId="somewhere-else" />).container).toBeEmptyDOMElement()
  })

  it('is a full card in the inbox and in the dock tray', () => {
    renderIn(wrap([ask()]), <PersonItemsInbox />)
    expect(screen.getByTestId('person-inbox')).toHaveTextContent('Needs you (1)')
    expect(screen.getByTestId('person-item-card')).toHaveAttribute('data-kind', 'ancestor_question')
    cleanup()
    renderIn(wrap([ask()]), <PersonItemTray />)
    fireEvent.click(screen.getByRole('button', { name: /need/ }))
    expect(screen.getByTestId('ask-question')).toBeInTheDocument()
  })

  it('is not a card without an ask id (a bare item is a row that opens the chat)', () => {
    renderIn(wrap([ask({ askId: 0 })]), <PersonItemsInbox />)
    expect(screen.queryByTestId('person-item-card')).toBeNull()
    expect(screen.getByTestId('person-inbox')).toHaveTextContent('ancestor_question')
  })

  it('turns the asking agent circle amber', () => {
    const S = (id, parent, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `Agent ${id}`, state: 'running', counts: {}, ...extra })
    const sessions = withWaiting([S(1, 0), S(31, 1)], [ask()])
    render(<OrbitRing orbit={orbitOf(sessions, { sessionId: 1 })} />)
    expect(screen.getByRole('button', { name: /Agent 31.*needs you/ })).toHaveAttribute('data-needs-you', 'true')
  })
})
