// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const App = {}
const list = (selected = 'typesafe') => ({
  selected,
  providers: [
    { id: 'typesafe', name: 'TypeSafe', models: ['jev-latest'], base_url: 'https://api.typesafe.ai', model: 'jev-latest', note: '', selected: selected === 'typesafe', supported: true, has_key: true },
    { id: 'liquid', name: 'Liquid AI', models: ['d1'], base_url: '', model: 'd1', note: '', selected: selected === 'liquid', supported: true, has_key: false },
    { id: 'tazo', name: 'Tazo Labs', models: ['tazo'], base_url: '', model: '', note: '', selected: false, supported: false, has_key: false },
  ],
})
beforeEach(() => {
  for (const k of ['JevListProviders', 'JevUseProvider', 'JevSetProviderEndpoint', 'JevSetProviderKey', 'JevRemoveProviderKey']) App[k] = vi.fn()
  App.JevListProviders.mockResolvedValue(list())
  App.JevUseProvider.mockResolvedValue()
  App.JevSetProviderEndpoint.mockResolvedValue()
  App.JevSetProviderKey.mockResolvedValue()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

async function mount() {
  const { default: Block } = await import('./DecisionProvidersBlock.jsx')
  render(<Block />)
  await screen.findByTestId('provider-liquid')
}

describe('DecisionProvidersBlock', () => {
  it('lists every provider and disables the ones without an adapter', async () => {
    await mount()
    expect(screen.getByLabelText('Use TypeSafe')).toBeChecked()
    expect(screen.getByLabelText('Use Tazo Labs')).toBeDisabled()
    expect(screen.getByTestId('provider-tazo')).toHaveTextContent('no adapter yet')
    expect(screen.getByTestId('provider-liquid')).toHaveTextContent('needs base URL')
  })

  it('selects a provider through the CLI binding', async () => {
    await mount()
    fireEvent.click(screen.getByLabelText('Use Liquid AI'))
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('liquid'))
  })

  it('saves the endpoint and key, and clears the key field', async () => {
    await mount()
    fireEvent.click(screen.getByTestId('provider-liquid').querySelector('button'))
    fireEvent.change(screen.getByLabelText('Liquid AI base URL'), { target: { value: 'https://x.example' } })
    fireEvent.change(screen.getByLabelText('Liquid AI API key'), { target: { value: 'sk-1' } })
    fireEvent.click(screen.getByText('Save'))
    await waitFor(() => expect(App.JevSetProviderKey).toHaveBeenCalledWith('liquid', 'sk-1'))
    expect(App.JevSetProviderEndpoint).toHaveBeenCalledWith('liquid', 'https://x.example', 'd1')
    await waitFor(() => expect(screen.getByLabelText('Liquid AI API key')).toHaveValue(''))
  })
})
