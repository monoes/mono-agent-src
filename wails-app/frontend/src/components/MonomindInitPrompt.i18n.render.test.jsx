// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../i18n.js'
import es from '../locales/es.json'
import MonomindInitPrompt from './MonomindInitPrompt.jsx'

vi.mock('../wailsjs/go/main/App', () => ({ InitializeMonomindProfile: vi.fn() }))

afterEach(() => { cleanup(); i18n.changeLanguage('en') })

describe('MonomindInitPrompt language', () => {
  it('shows its copy in the chosen language', async () => {
    await i18n.changeLanguage('es')
    render(<MonomindInitPrompt />)
    expect(screen.getByRole('button', { name: es.monomindInit.start })).toBeInTheDocument()
    expect(screen.getByText(es.monomindInit.title)).toBeInTheDocument()
    expect(screen.queryByText(/Set up agents/)).toBeNull()
  })
})
