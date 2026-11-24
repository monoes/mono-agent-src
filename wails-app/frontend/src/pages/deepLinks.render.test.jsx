// @vitest-environment jsdom
// W5 r30-7: the deep-link data App.jsx hands a page reaches the component that consumes it.
// Applications is covered in Applications.render.test.jsx; OrgsPanel's own consumption in OrgsPanel.render.test.jsx.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, cleanup } from '@testing-library/react'

const seen = vi.hoisted(() => ({ panel: null, shell: null }))
vi.mock('../components/OrgsPanel.jsx', () => ({ default: (p) => { seen.panel = p; return <div data-testid="panel" /> } }))
vi.mock('../components/tasksessions/TasksShell.jsx', () => ({ default: (p) => { seen.shell = p; return <div data-testid="shell" /> } }))

import Orgs from './Orgs.jsx'
import Tasks from './Tasks.jsx'
import { runHelpAction } from './Help/helpActions.js'
import { TOPICS } from './Help/helpContent.js'

afterEach(() => { cleanup(); seen.panel = null; seen.shell = null })

describe('deep links reach the page that consumes them', () => {
  it('Orgs hands the pending select (name, tab, run) and its consume callback to the panel', () => {
    const pending = { name: 'acme', tab: 'runs', run: 'r1' }
    const consume = vi.fn()
    render(<Orgs isActive pendingSelectOrgName={pending} onConsumePendingSelect={consume} onNavigate={vi.fn()} />)
    expect(seen.panel.pendingSelectOrgName).toBe(pending)
    expect(seen.panel.onConsumePendingSelect).toBe(consume)
    expect(seen.panel.pageActive).toBe(true)
  })

  it('Tasks forwards sessionId and report from navData to the shell', () => {
    render(<Tasks isActive navData={{ sessionId: 's-9', report: true }} />)
    expect(seen.shell.sessionHint).toBe('s-9')
    expect(seen.shell.reportHint).toBe(true)
  })

  it('Tasks without navData passes no hint', () => {
    render(<Tasks isActive navData={null} />)
    expect(seen.shell.sessionHint).toBeNull()
    expect(seen.shell.reportHint).toBe(false)
  })

  it('every Help goto step navigates with its own params', () => {
    for (const t of TOPICS) for (const s of t.steps) {
      if (s.action?.kind !== 'goto') continue
      const navigate = vi.fn()
      expect(runHelpAction(s.action, { navigate }), `${t.id}: ${s.action.target}`).toEqual({ ok: true })
      expect(navigate).toHaveBeenCalledWith(s.action.target, s.action.params)
    }
  })
})
