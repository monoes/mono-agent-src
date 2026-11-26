// @vitest-environment jsdom
import React from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'

vi.mock('../wailsjs/go/main/App', () => ({}))
import { ConvertControls } from './ImageEditorFullscreen.jsx'

afterEach(cleanup)

describe('image editor option cards', () => {
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
