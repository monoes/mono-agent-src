// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../../i18n.js'
import { ChatResetBar } from './ChatResetBar.jsx'

afterEach(cleanup)

describe('ChatResetBar', () => {
  it('calls the handlers from the Clear and Compact buttons', () => {
    const onClear = vi.fn()
    const onCompact = vi.fn()
    render(<ChatResetBar onClear={onClear} onCompact={onCompact} />)
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(onClear).toHaveBeenCalledTimes(1)
    expect(onCompact).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Compact' }))
    expect(onCompact).toHaveBeenCalledTimes(1)
  })

  it('disables each button on its own and ignores clicks while disabled', () => {
    const onClear = vi.fn()
    const onCompact = vi.fn()
    render(<ChatResetBar onClear={onClear} onCompact={onCompact} clearDisabled />)
    expect(screen.getByRole('button', { name: 'Clear' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Compact' })).not.toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(onClear).not.toHaveBeenCalled()
    cleanup()
    render(<ChatResetBar onClear={onClear} onCompact={onCompact} compactDisabled />)
    expect(screen.getByRole('button', { name: 'Compact' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Clear' })).not.toBeDisabled()
  })
})
