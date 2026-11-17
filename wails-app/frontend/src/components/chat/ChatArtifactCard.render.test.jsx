// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { ChatArtifactCard } from './ChatArtifactCard.jsx'

afterEach(cleanup)

describe('workflow artifact card', () => {
  const artifact = { type: 'workflow', id: 'wf-1', name: 'Daily digest' }

  it('offers Open as the primary action and keeps Copy ID', () => {
    const onOpen = vi.fn()
    render(<ChatArtifactCard artifact={artifact} onOpenArtifact={onOpen} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open workflow' }))
    expect(onOpen).toHaveBeenCalledWith(artifact)
    expect(screen.getByRole('button', { name: 'Copy workflow ID' })).toBeInTheDocument()
  })

  it('Copy ID does not open', () => {
    const onOpen = vi.fn()
    render(<ChatArtifactCard artifact={artifact} onOpenArtifact={onOpen} />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy workflow ID' }))
    expect(onOpen).not.toHaveBeenCalled()
  })
})
