// @vitest-environment jsdom
// #1766: the cards that approve an org's shell call show the command and the
// role's folder, as `org approvals` and `org chat history` report them.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import '../i18n.js'
import en from '../locales/en.json'
import es from '../locales/es.json'
import { AskCard } from './bubbles/AskCard.jsx'
import NeedsYouPanel from './orgs/NeedsYouPanel.jsx'
import { api } from '../services/api.js'

vi.mock('../services/api.js', () => ({
  api: { listNeedsYou: vi.fn(), getOrgQuestions: vi.fn(), getOrgGates: vi.fn(), getOrgApprovals: vi.fn(), approveOrgAction: vi.fn(), denyOrgAction: vi.fn() },
  notify: vi.fn(),
}))

beforeEach(() => {
  vi.clearAllMocks()
  api.listNeedsYou.mockResolvedValue({ error: 'unavailable' })
  api.getOrgQuestions.mockResolvedValue({ items: [] })
  api.getOrgGates.mockResolvedValue({ items: [] })
})
afterEach(cleanup)

const ask = { id: 'apr-1', ref: 'apr-1', kind: 'approval', role: 'writer', action: 'Bash', text: 'Approval required for Bash', pending: true }

describe('the bubble approval card', () => {
  it('shows the command and the role folder beside Approve', () => {
    render(<AskCard ask={{ ...ask, command: 'printf hi > "$TMPDIR/note.txt"', cwd: '/home/u/proj' }} />)
    const box = screen.getByTestId('approval-command')
    expect(within(box).getByText('printf hi > "$TMPDIR/note.txt"')).toBeInTheDocument()
    expect(box).toHaveTextContent('Role folder: /home/u/proj')
    expect(box).not.toHaveTextContent(en.ask.commandCut)
    expect(screen.getByTestId('org-chat-approve')).toBeInTheDocument()
  })

  it('warns when only the start of the command is known', () => {
    render(<AskCard ask={{ ...ask, command: 'curl …', command_truncated: true }} />)
    expect(screen.getByTestId('approval-command')).toHaveTextContent(en.ask.commandCut)
  })

  it('shows no command box for a call that has none', () => {
    render(<AskCard ask={{ ...ask, action: 'org_complete' }} />)
    expect(screen.queryByTestId('approval-command')).toBeNull()
  })
})

describe('the Needs you approval card', () => {
  it('shows every command one Approve would release', async () => {
    api.getOrgApprovals.mockResolvedValue({ items: [
      { roleId: 'writer', action: 'Bash', ts: 1, approved: null, requestId: 'apr-1', command: 'go test ./...', cwd: '/w' },
      { roleId: 'writer', action: 'Bash', ts: 2, approved: null, requestId: 'apr-2', command: 'git push --force', command_truncated: true, cwd: '/w' },
    ] })
    render(<NeedsYouPanel orgName="bud" />)
    const card = await screen.findByTestId('pending-item')
    const boxes = within(card).getAllByTestId('approval-command')
    expect(boxes).toHaveLength(2)
    expect(boxes[0]).toHaveTextContent('go test ./...')
    expect(boxes[0]).toHaveTextContent('Role folder: /w')
    expect(boxes[0]).not.toHaveTextContent(en.ask.commandCut)
    expect(boxes[1]).toHaveTextContent('git push --force')
    expect(boxes[1]).toHaveTextContent(en.ask.commandCut)
  })
})

describe('approval command wording', () => {
  it('exists in both locales', () => {
    for (const key of ['commandLabel', 'commandFolder', 'commandCut']) {
      expect(en.ask[key]).toBeTruthy()
      expect(es.ask[key]).toBeTruthy()
      expect(es.ask[key]).not.toBe(en.ask[key])
    }
  })
})
