// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const App = {}
beforeEach(() => {
  for (const k of ['GetExtensionInfo', 'InstallExtension']) App[k] = vi.fn()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const info = { path: '/home/u/.monoagent/extension', installed: true, current: true, version: '1.5.0' }

async function mount() {
  const { default: ExtensionSection } = await import('./ExtensionSection.jsx')
  render(<ExtensionSection />)
}

describe('ExtensionSection', () => {
  it('shows the folder to load and the steps to load it unpacked', async () => {
    App.GetExtensionInfo.mockResolvedValue(info)
    await mount()
    expect(await screen.findByTestId('extension-path')).toHaveTextContent(info.path)
    expect(screen.getByText(/Load unpacked/)).toBeInTheDocument()
    expect(screen.getByText(/Developer mode/)).toBeInTheDocument()
  })

  it('reinstalls on request and shows the answer', async () => {
    App.GetExtensionInfo.mockResolvedValue(info)
    App.InstallExtension.mockResolvedValue({ ...info, version: '1.6.0' })
    await mount()
    await screen.findByTestId('extension-path')
    fireEvent.click(screen.getByRole('button', { name: 'Reinstall' }))
    await waitFor(() => expect(screen.getByText(/Version 1.6.0/)).toBeInTheDocument())
  })

  it('shows the CLI error instead of a path', async () => {
    App.GetExtensionInfo.mockRejectedValue(new Error('sign in first'))
    await mount()
    expect(await screen.findByRole('alert')).toHaveTextContent('sign in first')
    expect(screen.queryByTestId('extension-path')).toBeNull()
  })
})
