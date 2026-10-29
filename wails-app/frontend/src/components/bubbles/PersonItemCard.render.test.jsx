// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import '../../i18n.js'
import { PersonItemCard } from './PersonItemCard.jsx'
import { getNotifySound, setNotifySound } from '../../lib/personChime.js'

afterEach(() => { cleanup(); localStorage.clear(); setNotifySound(true) })

const item = (extra = {}) => ({
  id: 4, kind: 'plan_approval', sessionId: 7, conversationId: 'c7', leadConversationId: 'c1',
  title: 'Port the importer', goal: 'Move every importer to the new API', state: 'draft',
  reasons: ['30 tasks, over 25', 'budget $10.00 is over 40% of the mission\'s $20.00 left'],
  taskCount: 30, budgetUsd: 10, budgetShare: 0.5, access: 'coding', ...extra,
})

describe('PersonItemCard', () => {
  it('shows the plan, why it needs the person and the numbers', () => {
    render(<PersonItemCard item={item()} onAnswer={vi.fn()} />)
    const card = screen.getByRole('group', { name: /Port the importer/ })
    expect(card).toHaveTextContent('A plan needs your approval')
    expect(card).toHaveTextContent('Move every importer to the new API')
    expect(card).toHaveTextContent('30 tasks, over 25')
    expect(card).toHaveTextContent('$10.00')
    expect(card).toHaveTextContent('50% of the mission')
    expect(screen.getByRole('button', { name: /Approve/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Deny/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Ask more/ })).toBeInTheDocument()
  })

  it('approves with one press', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ session: { state: 'running' } })
    render(<PersonItemCard item={item()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
    await waitFor(() => expect(onAnswer).toHaveBeenCalledWith('approve', expect.objectContaining({ sessionId: 7 }), undefined))
  })

  it('says the plan changed and asks for a new look when the approval is refused for a moved plan', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ error: 'the plan of session #7 changed since you read it', code: 'plan_changed' })
    render(<PersonItemCard item={item({ rev: 3, digest: 'abc' })} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The plan changed after you opened it')
  })

  it('denies with a note: the box takes focus, the send button needs words', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ session: { state: 'draft' } })
    render(<PersonItemCard item={item()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Deny/ }))
    const box = screen.getByRole('textbox')
    expect(box).toHaveFocus()
    const send = screen.getByRole('button', { name: /Send and keep as draft/ })
    expect(send).toBeDisabled()
    fireEvent.change(box, { target: { value: '  split it in two  ' } })
    fireEvent.click(send)
    await waitFor(() => expect(onAnswer).toHaveBeenCalledWith('deny', expect.objectContaining({ sessionId: 7 }), 'split it in two'))
  })

  it('asks more: the question is sent, the box closes and the card says the plan still waits', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ item: 'open' })
    render(<PersonItemCard item={item()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Ask more/ }))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'why 30 tasks?' } })
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(onAnswer).toHaveBeenCalledWith('ask', expect.anything(), 'why 30 tasks?'))
    expect(await screen.findByRole('status')).toHaveTextContent('still waiting for you')
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.getByRole('button', { name: /Approve/ })).toBeInTheDocument()
  })

  it('shows a refusal where it was asked and keeps what was typed', async () => {
    const onAnswer = vi.fn().mockResolvedValue({ error: 'only the operator can answer a person item' })
    render(<PersonItemCard item={item()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Deny/ }))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'no' } })
    fireEvent.click(screen.getByRole('button', { name: /Send and keep as draft/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('only the operator can answer a person item')
    expect(screen.getByRole('textbox')).toHaveValue('no')
  })

  it('shows a failed approve and lets the person try again', async () => {
    const onAnswer = vi.fn().mockResolvedValueOnce({ error: 'no open person item asks for the approval of session #7' }).mockResolvedValueOnce({ session: {} })
    render(<PersonItemCard item={item()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('no open person item')
    fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
    await waitFor(() => expect(onAnswer).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  })

  it('Escape closes the box without sending and gives focus back to the button', () => {
    const onAnswer = vi.fn()
    render(<PersonItemCard item={item()} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: /Ask more/ }))
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' })
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.getByRole('button', { name: /Ask more/ })).toHaveFocus()
    expect(onAnswer).not.toHaveBeenCalled()
  })

  it('renders agent-written text as text, not markup', () => {
    render(<PersonItemCard item={item({ title: '<img src=x onerror=alert(1)>', goal: '<b>bold</b>' })} onAnswer={vi.fn()} />)
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('.pi-goal b')).toBeNull()
  })

  it('has a mute toggle that flips the notifySound setting', () => {
    render(<PersonItemCard item={item()} onAnswer={vi.fn()} />)
    const mute = screen.getByRole('button', { name: 'Mute the chime' })
    expect(mute).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(mute)
    expect(getNotifySound()).toBe(false)
    expect(screen.getByRole('button', { name: 'Unmute the chime' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('opens the agent when asked to', () => {
    const onOpenAgent = vi.fn()
    render(<PersonItemCard item={item()} onAnswer={vi.fn()} onOpenAgent={onOpenAgent} />)
    fireEvent.click(screen.getByRole('button', { name: /Open this agent/ }))
    expect(onOpenAgent).toHaveBeenCalledWith(expect.objectContaining({ conversationId: 'c7' }))
  })
})
