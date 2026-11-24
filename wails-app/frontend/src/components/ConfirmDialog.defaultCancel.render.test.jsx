// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act } from '@testing-library/react'
import ConfirmHost, { confirm } from './ConfirmDialog.jsx'

afterEach(cleanup)

async function open(opts) {
  render(<ConfirmHost />)
  let answer
  await act(async () => { answer = confirm('Risky?', { title: 'Risky', confirmLabel: 'Do it', ...opts }) })
  return { answer }
}

describe('ConfirmDialog defaultCancel', () => {
  it('puts focus on Cancel and a bare Enter does not confirm', async () => {
    const { answer } = await open({ defaultCancel: true })
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()
    fireEvent.keyDown(document.body, { key: 'Enter' })
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(await answer).toBe(false)
  })

  it('without the option the confirm button keeps focus', async () => {
    const { answer } = await open({})
    expect(screen.getByRole('button', { name: 'Do it' })).toHaveFocus()
    fireEvent.click(screen.getByRole('button', { name: 'Do it' }))
    expect(await answer).toBe(true)
  })

  it('the confirm button still works by click when defaultCancel is set', async () => {
    const { answer } = await open({ defaultCancel: true })
    fireEvent.click(screen.getByRole('button', { name: 'Do it' }))
    expect(await answer).toBe(true)
  })
})
