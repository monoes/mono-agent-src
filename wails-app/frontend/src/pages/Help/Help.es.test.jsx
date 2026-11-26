// @vitest-environment jsdom
import React from 'react'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import i18n from '../../i18n.js'
import Help from './Help.jsx'
import { HELP_KEYS, PIT_STAGES, TOPICS } from './helpContent.js'

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))
afterEach(cleanup)

describe('Help in Spanish', () => {
  it('shows topics, steps and the pit in Spanish, and follows a language change', async () => {
    render(<Help onNavigate={() => {}} />)
    fireEvent.click(screen.getByText('Primeros pasos'))
    expect(screen.getByText(/Primero inicia sesión en monoes\.me/)).toBeInTheDocument()
    fireEvent.change(screen.getByRole('slider'), { target: { value: '1' } })
    expect(screen.getByText('El felpudo de bienvenida')).toBeInTheDocument()
    await act(() => i18n.changeLanguage('en'))
    expect(screen.getByText('First steps')).toBeInTheDocument()
    expect(screen.getByText('The Welcome Mat')).toBeInTheDocument()
    await act(() => i18n.changeLanguage('es'))
  })

  it('has Spanish text for every topic, step, coder prompt and pit stage', async () => {
    const read = () => [
      ...TOPICS.flatMap(t => [t.title, t.blurb, t.quip, t.danger?.quip, ...t.steps.flatMap(s => [s.text, s.action?.prompt])]),
      ...PIT_STAGES.flatMap(s => [s.permission, s.label, s.consequence]),
    ].filter(Boolean)
    const spanish = read()
    await act(() => i18n.changeLanguage('en'))
    const english = read()
    await act(() => i18n.changeLanguage('es'))
    expect(spanish).toHaveLength(english.length)
    expect(english.length).toBeGreaterThan(90)
    for (const [i, text] of spanish.entries()) {
      expect(text).not.toMatch(/^ui6/)
      if (text !== 'Perfiles') expect(text).not.toBe(english[i])
    }
    expect(Object.keys(HELP_KEYS).length).toBeGreaterThan(100)
  })
})
