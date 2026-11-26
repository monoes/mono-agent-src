// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import '../../i18n.js'

// A typed key belongs to the host it was typed for: it does not follow the
// add form to another preset, kind or host.
const App = {}

beforeEach(() => {
  App.JevAddProvider = vi.fn().mockResolvedValue({ name: 'added' })
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

async function typed() {
  const { default: Form } = await import('./JevAddProviderForm.jsx')
  render(<Form kinds={['openai', 'anthropic']} busy={false} run={(_, fn) => fn()} reload={async () => {}} onClose={() => {}} />)
  const form = screen.getByTestId('jev-add-provider')
  const field = label => within(form).getByLabelText(label)
  const set = (label, value) => fireEvent.change(field(label), { target: { value } })
  set('Preset', 'openai')
  set('API key', 'fake-openai-key')
  return { set, key: () => field('API key') }
}

describe('the key typed in the add provider form', () => {
  for (const [what, label, value] of [
    ['another preset', 'Preset', 'openrouter'],
    ['another preset on the same host', 'Preset', 'openaiDecisions'],
    ['another kind', 'Kind', 'anthropic'],
    ['a base URL on another host', 'Base URL', 'https://llm.example.com/v1'],
    ['a base URL on another port', 'Base URL', 'https://api.openai.com:8443/v1'],
    ['a base URL over another scheme', 'Base URL', 'http://api.openai.com/v1'],
    ['a base URL being retyped', 'Base URL', 'https://api.openai.co'],
  ]) {
    it(`is cleared by ${what}`, async () => {
      const { set, key } = await typed()
      set(label, value)
      expect(screen.getByLabelText(/^API key/)).toHaveValue('')
    })
  }

  it('stays while the host does', async () => {
    const { set, key } = await typed()
    set('Base URL', 'https://API.openai.com/v2')
    set('Model', 'gpt-4o')
    set('Name', 'mine')
    expect(key()).toHaveValue('fake-openai-key')
  })
})
