// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../../i18n.js'

vi.mock('../../services/api.js', () => ({ api: { openURL: vi.fn(), coderStopBackground: vi.fn() } }))

import { StageDrawer } from './StageDrawer.jsx'

const node = {
  id: 'w1', role: 'Tester', status: 'done', runtime: 'claude', model: 'sonnet',
  prevModel: { runtime: 'codex', model: '', reason: 'sin cupo' },
  skills: [], files: [], messages: [], parts: [], callOrder: [], tools: 0,
}

describe('stage drawer in Spanish', () => {
  beforeAll(async () => { await i18n.changeLanguage('es') })
  afterAll(async () => { await i18n.changeLanguage('en') })
  afterEach(cleanup)

  it('names the model a reassigned agent had when it named none', () => {
    render(<StageDrawer node={node} onClose={() => {}} />)
    expect(screen.getByText(/codex\/predeterminado/)).toBeInTheDocument()
  })
})
