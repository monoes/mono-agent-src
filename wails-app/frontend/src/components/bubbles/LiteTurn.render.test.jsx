// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import '../../i18n.js'
import { LiteTurn } from './LiteTurn.jsx'

afterEach(cleanup)

const state = {
  parts: [{ kind: 'text', partId: 'p1', text: 'Working.' }, { kind: 'tool', callId: 'k1' }],
  calls: { k1: { callId: 'k1', name: 'Bash', arguments: { command: 'curl -s https://example.test/a/very/long/path/that/would/widen/a/narrow/pane' }, status: 'completed', ok: true, result: 'ok' } },
}

describe('LiteTurn', () => {
  it('draws a tool call as one collapsed chip that opens on demand', () => {
    render(<LiteTurn state={state} turnId="t1" />)
    const chip = screen.getByRole('button', { name: /Bash/ })
    expect(chip).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(chip)
    expect(chip).toHaveAttribute('aria-expanded', 'true')
  })
  it('never grows past its pane, so a long call cannot push the transcript sideways', () => {
    render(<LiteTurn state={state} turnId="t1" />)
    expect(screen.getByTestId('lite-turn').style.maxWidth).toBe('100%')
  })
})
