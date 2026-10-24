// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { analyzeRecording: vi.fn(() => new Promise(() => {})), verifyDraft: vi.fn(), saveDraft: vi.fn() } }))
vi.mock('../services/api.js', () => ({ api, notify: vi.fn() }))

import RunProblemPanel from './RunProblemPanel.jsx'
import RecordingReview from '../pages/connections/RecordingReview.jsx'

afterEach(cleanup)

// failed-run-fix and recording-analyze run as confined tasks that only
// propose, so their buttons are always offered.
describe('the confined kind buttons', () => {
  it('Fix with coder is offered on a run problem', () => {
    const problem = { status: 'FAILED', error: 'x', failed: [] }
    render(<RunProblemPanel problem={problem} onClose={() => {}} onSendToCoder={() => ({ goal: 'g', origin: { page: 'noderunner', label: 'Workflows' } })} />)
    expect(screen.getByRole('button', { name: /Fix with coder/ })).toBeInTheDocument()
  })

  it('Discuss with coder is offered on a recording review', () => {
    render(<RecordingReview recording={{ id: 'r1', title: 'T' }} automationId="a" onBack={() => {}} />)
    expect(screen.getByRole('button', { name: /Discuss with coder/ })).toBeInTheDocument()
  })
})
