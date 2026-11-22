// @vitest-environment jsdom
import React from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'

vi.mock('../wailsjs/go/main/App', () => ({}))
import { RemoveBgControls, ConvertControls } from './ImageEditorFullscreen.jsx'

afterEach(cleanup)

describe('image editor option cards', () => {
  it('background cards are buttons that say which one is chosen', () => {
    render(<RemoveBgControls bgcolor="" setBgcolor={() => {}} />)
    const transparent = screen.getByRole('button', { name: /Transparent/ })
    const color = screen.getByRole('button', { name: /^Color/ })
    expect(transparent).toHaveAttribute('aria-pressed', 'true')
    expect(color).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(color)
    expect(screen.getByRole('button', { name: /^Color/ })).toHaveAttribute('aria-pressed', 'true')
  })

  it('output format cards are buttons that choose the format', () => {
    const setFormat = vi.fn()
    render(<ConvertControls format="png" setFormat={setFormat} quality={80} setQuality={() => {}} sizeBytes={1000} />)
    expect(screen.getByRole('button', { name: /PNG/ })).toHaveAttribute('aria-pressed', 'true')
    const jpeg = screen.getByRole('button', { name: /JPEG/ })
    expect(jpeg).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(jpeg)
    expect(setFormat).toHaveBeenCalledWith('jpeg')
  })
})
