// @vitest-environment jsdom
import React, { useState } from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, fireEvent, screen } from '@testing-library/react'
import { useEscapeClose } from './escapeStack.js'
import ConfirmHost, { confirm } from '../components/ConfirmDialog.jsx'
import { act } from '@testing-library/react'

afterEach(cleanup)

function Modal({ name, onClose, active = true }) {
  useEscapeClose(onClose, active)
  return <div>{name}</div>
}

function Stacked({ outer, inner }) {
  const [open, setOpen] = useState(true)
  return (
    <>
      <Modal name="outer" onClose={outer} />
      {open && <Modal name="inner" onClose={() => { inner(); setOpen(false) }} />}
    </>
  )
}

describe('useEscapeClose', () => {
  it('closes only the topmost of stacked modals, then the next one', () => {
    const outer = vi.fn(), inner = vi.fn()
    render(<Stacked outer={outer} inner={inner} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(inner).toHaveBeenCalledTimes(1)
    expect(outer).not.toHaveBeenCalled()
    expect(screen.queryByText('inner')).toBeNull()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(outer).toHaveBeenCalledTimes(1)
  })

  it('ignores other keys and inactive modals', () => {
    const a = vi.fn(), b = vi.fn()
    render(<><Modal name="a" onClose={a} /><Modal name="b" onClose={b} active={false} /></>)
    fireEvent.keyDown(window, { key: 'Enter' })
    expect(a).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).not.toHaveBeenCalled()
  })

  it('stops listening once every modal is gone', () => {
    const a = vi.fn()
    const { unmount } = render(<Modal name="a" onClose={a} />)
    unmount()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(a).not.toHaveBeenCalled()
  })

  it('uses the latest handler without re-registering', () => {
    const first = vi.fn(), second = vi.fn()
    const { rerender } = render(<Modal name="a" onClose={first} />)
    rerender(<Modal name="a" onClose={second} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
  })

  it('Escape on a confirm opened over a modal answers no and leaves the modal open', async () => {
    const modalClose = vi.fn()
    render(<><Modal name="modal" onClose={modalClose} /><ConfirmHost /></>)
    let answer
    await act(async () => { answer = confirm('Really?') })
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(await answer).toBe(false)
    expect(modalClose).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(modalClose).toHaveBeenCalledTimes(1)
  })
})
