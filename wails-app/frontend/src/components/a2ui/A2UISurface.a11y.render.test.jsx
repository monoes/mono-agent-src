// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import '../../i18n.js'
import { A2UISurface } from './A2UISurface.jsx'
import { applySurfaceEvent } from '../../lib/a2uiModel.js'
import { hilApproval, structuredQuestion, missionPlan } from './fixtures.js'

afterEach(cleanup)
const fold = payloads => payloads.reduce((s, p, i) => applySurfaceEvent(s, p, i + 1), [])[0]
const mk = (components, extra = {}) => ({ surfaceId: 's', provenance: 'agent', components: Object.fromEntries(components.map(c => [c.id, c])), dataModel: {}, status: 'live', lastSeq: 1, ...extra })

describe('A2UI card accessibility', () => {
  it('is a region named from who sent it and its first text', () => {
    render(<><A2UISurface surface={fold(hilApproval)} /><A2UISurface surface={fold(structuredQuestion)} /></>)
    expect(screen.getByRole('region', { name: 'from monoagent: Comment on LinkedIn post' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'from agent: Which audience should the outreach target?' })).toBeInTheDocument()
  })

  it('labels every input and groups choices under the question', () => {
    render(<><A2UISurface surface={fold(hilApproval)} /><A2UISurface surface={fold(structuredQuestion)} /></>)
    expect(screen.getByRole('textbox', { name: 'Reply' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Optional note' })).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'I checked the text' })).toBeInTheDocument()
    const group = screen.getByRole('group', { name: 'Which audience should the outreach target?' })
    expect(group.tagName).toBe('FIELDSET')
    expect(within(group).getAllByRole('radio')).toHaveLength(3)
    for (const el of document.querySelectorAll('input, textarea')) expect(el.labels.length).toBeGreaterThan(0)
  })

  it('names an input or picker that came with no label or question', () => {
    render(<A2UISurface surface={mk([
      { id: 'root', component: 'Column', children: ['f', 'p'] },
      { id: 'f', component: 'TextField', label: '', value: { '@path': '/a' } },
      { id: 'p', component: 'ChoicePicker', variant: 'multipleSelection', value: { '@path': '/b' }, options: [{ label: 'x', value: 'x' }] },
    ])} />)
    expect(screen.getByRole('textbox', { name: 'Text field' })).toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Choose any' })).toBeInTheDocument()
  })

  it('exposes progress with name, value, max and text', () => {
    render(<A2UISurface surface={fold(missionPlan)} />)
    const bar = screen.getByRole('progressbar', { name: 'Budget used (USD)' })
    expect(bar).toHaveAttribute('aria-valuenow', '1.8')
    expect(bar).toHaveAttribute('aria-valuemax', '5')
    expect(bar).toHaveAttribute('aria-valuetext', '1.8 of 5')
    cleanup()
    render(<A2UISurface surface={mk([{ id: 'root', component: 'Progress', value: 3 }])} />)
    expect(screen.getByRole('progressbar', { name: 'Progress' })).toBeInTheDocument()
  })

  it('says a badge tone in words and with a mark, not by colour alone', () => {
    render(<A2UISurface surface={mk([{ id: 'root', component: 'Row', children: ['a', 'b'] },
      { id: 'a', component: 'Badge', text: 'running', tone: 'warning' }, { id: 'b', component: 'Badge', text: 'plain' }])} />)
    const badge = screen.getByText('running').parentElement
    expect(badge).toHaveTextContent('!Warning: running')
    expect(badge.querySelector('[aria-hidden="true"]')).toHaveTextContent('!')
    expect(screen.getByText('plain').parentElement).toHaveTextContent(/^plain$/)
  })

  it('announces layout changes and answers, not progress ticks', () => {
    const base = fold(missionPlan)
    const { rerender } = render(<A2UISurface surface={base} />)
    const said = () => screen.getByRole('status').textContent
    expect(said()).toBe('')
    rerender(<A2UISurface surface={{ ...base, dataModel: { spent: 3, budget: 5 }, lastSeq: 2 }} />)
    expect(said()).toBe('')
    rerender(<A2UISurface surface={{ ...base, components: { ...base.components, t1b: { id: 't1b', component: 'Badge', text: 'failed', tone: 'danger' } }, lastSeq: 3 }} />)
    expect(said()).toBe('Card updated: Plan: grow newsletter list (3 tasks)')
    rerender(<A2UISurface surface={{ ...base, status: 'resolved', lastSeq: 4 }} />)
    expect(said()).toBe('Card answered: Plan: grow newsletter list (3 tasks)')
    // The card body itself is not a live region.
    expect(screen.getByText('Collect leads').closest('[aria-live]')).toBeNull()
  })

  it('keeps focus on a pressed button and says why it is off', () => {
    const onAction = vi.fn()
    render(<A2UISurface surface={fold(hilApproval)} onAction={onAction} />)
    const b = screen.getByRole('button', { name: 'Approve and send' })
    b.focus()
    fireEvent.click(b); fireEvent.click(b)
    expect(onAction).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(b)
    expect(b).toHaveAttribute('aria-disabled', 'true')
    expect(b).toHaveAccessibleDescription('Sent. Waiting for a response.')
  })

  it('a settled card gives the reason and turns its inputs off', () => {
    render(<A2UISurface surface={{ ...fold(hilApproval), status: 'resolved' }} />)
    expect(screen.getByRole('button', { name: 'Reject' })).toHaveAccessibleDescription('Already answered. These buttons are off.')
    expect(screen.getByRole('textbox', { name: 'Reply' })).toHaveAttribute('readonly')
    expect(screen.getByRole('checkbox')).toBeDisabled()
  })

  it('filters a filterable picker without hiding what is chosen', () => {
    const onAction = vi.fn()
    render(<A2UISurface onAction={onAction} surface={mk([
      { id: 'root', component: 'Column', children: ['p', 'go'] },
      { id: 'p', component: 'ChoicePicker', filterable: true, variant: 'multipleSelection', value: { '@path': '/v' },
        options: ['Apple', 'Banana', 'Cherry'].map(x => ({ label: x, value: x.toLowerCase() })) },
      { id: 'l', component: 'Text', text: 'Go' },
      { id: 'go', component: 'Button', child: 'l', action: { event: { name: 'go', context: { v: { '@path': '/v' } } } } },
    ])} />)
    fireEvent.click(screen.getByRole('checkbox', { name: 'Apple' }))
    const box = screen.getByRole('searchbox', { name: 'Filter options' })
    fireEvent.change(box, { target: { value: 'ban' } })
    expect(screen.getAllByRole('checkbox').map(c => c.parentElement.textContent)).toEqual(['Apple', 'Banana'])
    expect(screen.getByText('2 of 3 options shown')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Go' }))
    expect(onAction.mock.calls[0][0].action.context).toEqual({ v: ['apple'] })
  })

  it('has no filter box unless asked', () => {
    render(<A2UISurface surface={fold(structuredQuestion)} />)
    expect(screen.queryByRole('searchbox')).toBeNull()
  })
})
