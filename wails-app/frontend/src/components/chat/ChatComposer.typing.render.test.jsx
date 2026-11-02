// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { useState } from 'react'
import { render, screen, fireEvent, act, cleanup } from '@testing-library/react'
import { ChatComposer } from './ChatComposer.jsx'

// The draft lives in a parent above the transcript (the overlays), so a
// composer that reports every keystroke re-renders the whole transcript per key.
let listRenders = 0
function Transcript() { listRenders++; return <div data-testid="list" /> }
function Harness({ onSend = () => {} }) {
  const [draft, setDraft] = useState('')
  return (
    <>
      <Transcript />
      <ChatComposer value={draft} onChange={setDraft} onSend={onSend} onStop={() => {}} streaming={false} disabled={false} />
      <button onClick={() => setDraft('')}>clear</button>
    </>
  )
}
const box = () => screen.getByPlaceholderText('Type a message...')
const type = (s) => { for (const ch of s) fireEvent.change(box(), { target: { value: box().value + ch } }) }
const backspace = (n) => { for (let i = 0; i < n; i++) fireEvent.change(box(), { target: { value: box().value.slice(0, -1) } }) }

afterEach(() => { cleanup(); vi.useRealTimers(); listRenders = 0 })

describe('ChatComposer typing', () => {
  it('keeps typing and held Backspace exact without re-rendering the transcript per key', () => {
    vi.useFakeTimers()
    render(<Harness />)
    const base = listRenders
    type('the quick brown fox jumps over the lazy dog')
    backspace(20)
    expect(box().value).toBe('the quick brown fox jum')
    expect(listRenders - base).toBeLessThanOrEqual(1)
    act(() => { vi.advanceTimersByTime(300) })
    expect(box().value).toBe('the quick brown fox jum')
    expect(listRenders - base).toBeLessThanOrEqual(2)
  })

  it('hands the exact text to onSend before the parent has heard of it, then clears', () => {
    vi.useFakeTimers()
    const onSend = vi.fn()
    render(<Harness onSend={onSend} />)
    type('hello')
    fireEvent.keyDown(box(), { key: 'Enter' })
    expect(onSend).toHaveBeenCalledWith('hello')
  })

  it('takes an outside change of the draft', () => {
    vi.useFakeTimers()
    render(<Harness />)
    type('abc')
    act(() => { vi.advanceTimersByTime(300) })
    fireEvent.click(screen.getByText('clear'))
    expect(box().value).toBe('')
  })

  it('pushes pending text up on unmount', () => {
    vi.useFakeTimers()
    const onChange = vi.fn()
    const { unmount } = render(<ChatComposer value="" onChange={onChange} onSend={() => {}} onStop={() => {}} streaming={false} disabled={false} />)
    fireEvent.change(box(), { target: { value: 'kept' } })
    expect(onChange).not.toHaveBeenCalled()
    unmount()
    expect(onChange).toHaveBeenCalledWith('kept')
  })
})
