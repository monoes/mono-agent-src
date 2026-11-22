// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import '../../i18n.js'
import SetupSteps from './SetupSteps.jsx'

afterEach(cleanup)

const report = { results: [
  { id: 'core.db', title: 'Database', status: 'fail', fix: { id: 'core.db.migrate', label: 'Migrate', safety: 'auto' } },
  { id: 'runtimes.agents', title: 'AI agent runtimes', status: 'fail' },
] }

describe('SetupSteps colours', () => {
  it('uses the theme tokens, not hard-coded cyan', () => {
    const { container } = render(<SetupSteps report={report} level="broken" fixStates={{}} onFix={() => {}} onNavigate={() => {}} onRunAll={() => {}} />)
    const styles = [...container.querySelectorAll('[style]')].map(e => e.getAttribute('style')).join('\n')
    expect(styles).not.toMatch(/#00b4d8|rgba\(\s*0,\s*180,\s*216/i)
  })
})
