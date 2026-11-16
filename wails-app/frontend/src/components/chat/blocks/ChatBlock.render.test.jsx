// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'
import { ChatBlock } from './ChatBlock.jsx'
import { ChatBlocksScope } from './context.js'
import { str } from './schema.js'

afterEach(cleanup)

const blocks = { demo: { validate: (j) => ({ t: str(j.t) }), Component: ({ data }) => <b data-testid="demo">{data.t}</b> } }
const fb = <pre data-testid="fb">fallback</pre>
const live = (v) => ({ scope: 's', live: v })

describe('ChatBlock boundary', () => {
  it('renders a valid block', () => {
    render(<ChatBlock info="demo" body='{"t":"hi"}' offset={0} fallback={fb} blocks={blocks} />)
    expect(screen.getByTestId('demo')).toHaveTextContent('hi')
  })
  it('falls back for unknown and invalid blocks', () => {
    render(<><ChatBlock info="nope" body="{}" fallback={fb} blocks={blocks} /><ChatBlock info="demo" body='{"t":1}' fallback={fb} blocks={blocks} /></>)
    expect(screen.getAllByTestId('fb')).toHaveLength(2)
  })
  it('shows a skeleton while streaming and the fallback once the turn ended', () => {
    const { rerender } = render(<ChatBlocksScope.Provider value={live(true)}><ChatBlock info="demo" body='{"t":"h' fallback={fb} blocks={blocks} /></ChatBlocksScope.Provider>)
    expect(screen.getByTestId('chat-block-skeleton')).toBeInTheDocument()
    expect(screen.queryByTestId('fb')).toBeNull()
    rerender(<ChatBlocksScope.Provider value={live(false)}><ChatBlock info="demo" body='{"t":"h' fallback={fb} blocks={blocks} /></ChatBlocksScope.Provider>)
    expect(screen.getByTestId('fb')).toBeInTheDocument()
  })
  it('falls back when the component throws', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const boom = { demo: { validate: () => ({}), Component: () => { throw new Error('x') } } }
    render(<ChatBlock info="demo" body="{}" fallback={fb} blocks={boom} />)
    expect(screen.getByTestId('fb')).toBeInTheDocument()
  })
})

describe('ChatMarkdown hook', () => {
  it('leaves ordinary and unknown fences as the code block', () => {
    render(<ChatMarkdown content={'```js\nconst a = 1\n```\n\n```chart\n{broken\n```'} />)
    expect(screen.getByText('const a = 1')).toBeInTheDocument()
    expect(screen.getByText('{broken')).toBeInTheDocument()
  })
})
