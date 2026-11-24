// @vitest-environment jsdom
// W4 R21-11: a late answer for an old id is dropped; a failed load says so and can be retried.
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'

const detail = vi.fn()
vi.mock('../services/api.js', () => ({
  api: { getPostDetail: (...a) => detail(...a), getPostComments: () => Promise.resolve([]) },
}))
import PostDetail from './PostDetail.jsx'

afterEach(() => { cleanup(); detail.mockReset() })

describe('PostDetail', () => {
  it('ignores a slow response for an earlier id', async () => {
    let resolveA
    detail.mockImplementation(id => id === 'a'
      ? new Promise(r => { resolveA = r })
      : Promise.resolve({ shortcode: 'POST-B', url: 'https://x.test/b' }))
    const { rerender } = render(<PostDetail id="a" onBack={() => {}} onOpenURL={() => {}} />)
    rerender(<PostDetail id="b" onBack={() => {}} onOpenURL={() => {}} />)
    await screen.findByText('POST-B')
    await act(async () => { resolveA({ shortcode: 'POST-A', url: 'https://x.test/a' }) })
    expect(screen.getByText('POST-B')).toBeInTheDocument()
    expect(screen.queryByText('POST-A')).toBeNull()
  })

  it('shows the error and retries instead of claiming the post is missing', async () => {
    detail.mockRejectedValueOnce(new Error('cli down'))
    detail.mockResolvedValueOnce({ shortcode: 'POST-OK', url: 'https://x.test/ok' })
    render(<PostDetail id="a" onBack={() => {}} onOpenURL={() => {}} />)
    await screen.findByRole('alert')
    expect(screen.getByRole('alert')).toHaveTextContent('cli down')
    expect(screen.queryByText('Post not found.')).toBeNull()
    fireEvent.click(screen.getByText('Retry'))
    await screen.findByText('POST-OK')
  })
})
