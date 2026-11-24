// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k) => k }) }))
vi.mock('../../services/api.js', () => ({
  api: { listOrgGrants: vi.fn(() => Promise.resolve({ grants: [] })), getOrgGates: vi.fn(() => Promise.resolve({ gates: [] })) },
}))

import FullAutoConfirm from './FullAutoConfirm.jsx'

afterEach(cleanup)

describe('FullAutoConfirm Escape', () => {
  it('Escape cancels while open, and does nothing while closed', () => {
    const onCancel = vi.fn()
    const { rerender } = render(<FullAutoConfirm open={false} orgName="o" onConfirm={() => {}} onCancel={onCancel} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onCancel).not.toHaveBeenCalled()
    rerender(<FullAutoConfirm open orgName="o" onConfirm={() => {}} onCancel={onCancel} />)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalledTimes(1)
  })
})
