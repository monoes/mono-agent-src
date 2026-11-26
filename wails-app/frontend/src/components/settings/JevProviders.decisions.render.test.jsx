// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'

// The two native decision-API kinds in the add-provider form (#1740): a
// preset each, and a key even for an address on this computer, as the CLI
// requires.
const App = {}
const KINDS = ['anthropic', 'gemini-decisions', 'hf-zeroshot', 'openai', 'openai-decisions']

beforeEach(() => {
  App.JevAddProvider = vi.fn().mockResolvedValue({ name: 'added' })
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

async function mount() {
  const { default: Form } = await import('./JevAddProviderForm.jsx')
  render(<Form kinds={KINDS} busy={false} run={(_, fn) => fn()} reload={async () => {}} onClose={() => {}} />)
  return screen.getByTestId('jev-add-provider')
}

describe('Add a decisions provider', () => {
  for (const [preset, kind, base, model] of [
    ['openaiDecisions', 'openai-decisions', 'https://api.openai.com', 'gpt-6-luna'],
    ['geminiDecisions', 'gemini-decisions', 'https://generativelanguage.googleapis.com', 'gemini-decision-1'],
  ]) {
    it(`${kind}: the preset fills the vendor's host and model and needs a key`, async () => {
      const form = await mount()
      fireEvent.change(within(form).getByLabelText('Preset'), { target: { value: preset } })
      expect(within(form).getByLabelText('Kind')).toHaveValue(kind)
      expect(within(form).getByLabelText('Base URL')).toHaveValue(base)
      expect(within(form).getByLabelText('Model')).toHaveValue(model)
      expect(within(form).getByLabelText('Name')).toHaveValue(kind)
      const add = within(form).getByRole('button', { name: 'Add provider' })
      expect(add).toBeDisabled()
      fireEvent.change(within(form).getByLabelText('API key'), { target: { value: 'fake-vendor-key' } })
      fireEvent.click(add)
      await waitFor(() => expect(App.JevAddProvider).toHaveBeenCalledWith(expect.objectContaining({
        name: kind, kind, base_url: base, model, api_key: 'fake-vendor-key',
      })))
    })
  }

  it('asks for a key on this computer too', async () => {
    const form = await mount()
    // The first preset is a server on this computer: no key needed for "openai".
    const add = within(form).getByRole('button', { name: 'Add provider' })
    expect(add).toBeEnabled()
    fireEvent.change(within(form).getByLabelText('Kind'), { target: { value: 'openai-decisions' } })
    expect(add).toBeDisabled()
    expect(within(form).getByText('This kind of provider always needs a key.')).toBeInTheDocument()
    expect(within(form).queryByText(/may be keyless/)).not.toBeInTheDocument()
    fireEvent.change(within(form).getByLabelText('API key'), { target: { value: 'fake-vendor-key' } })
    expect(add).toBeEnabled()
  })
})
