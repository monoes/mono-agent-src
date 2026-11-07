// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../../i18n.js'
import { OpenRunNotice, firstTurnFailedIn } from './OpenRunNotice.jsx'

afterEach(cleanup)

describe('the first-turn notice', () => {
  it('reads the failed first turn from the start document, and nothing else', () => {
    expect(firstTurnFailedIn({ output: '{"run_id":"p/1","first_turn":"failed"}' })).toBe(true)
    expect(firstTurnFailedIn({ output: '{"run_id":"p/1","first_turn":"queued"}' })).toBe(false)
    expect(firstTurnFailedIn({ output: 'not json' })).toBe(false)
    expect(firstTurnFailedIn(null)).toBe(false)
  })
  it('presses the header Stop when it is there', () => {
    const stop = vi.fn()
    render(<div data-testid="bubble-overlay"><span data-testid="chat-stop"><button onClick={stop}>Stop</button></span><OpenRunNotice onDismiss={() => {}} /></div>)
    fireEvent.click(screen.getByRole('button', { name: 'Stop the run' }))
    expect(stop).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('open-run-notice-nostop')).toBeNull()
  })
  it('says where else to stop when the header Stop is not there, instead of doing nothing', () => {
    render(<div data-testid="bubble-overlay"><OpenRunNotice onDismiss={() => {}} /></div>)
    fireEvent.click(screen.getByRole('button', { name: 'Stop the run' }))
    expect(screen.getByTestId('open-run-notice-nostop')).toHaveTextContent('Control room')
  })
  it('says the same when the header Stop is disabled (a stop is already going)', () => {
    render(<div data-testid="bubble-overlay"><span data-testid="chat-stop"><button disabled>Stop</button></span><OpenRunNotice onDismiss={() => {}} /></div>)
    fireEvent.click(screen.getByRole('button', { name: 'Stop the run' }))
    expect(screen.getByTestId('open-run-notice-nostop')).toBeInTheDocument()
  })
})

describe('dismissing', () => {
  it('calls onDismiss from the Dismiss button', () => {
    const onDismiss = vi.fn()
    render(<OpenRunNotice onDismiss={onDismiss} />)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })
})
