// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { useMountedTimers } from './useMountedTimers'

afterEach(() => { cleanup(); vi.useRealTimers() })

function Probe({ onTimeout, onInterval }) {
  const t = useMountedTimers()
  return <button onClick={() => { t.timeout(onTimeout, 100); t.interval(onInterval, 50) }}>go</button>
}

describe('useMountedTimers', () => {
  it('fires while mounted', () => {
    vi.useFakeTimers()
    const a = vi.fn(), b = vi.fn()
    const { getByText } = render(<Probe onTimeout={a} onInterval={b} />)
    getByText('go').click()
    vi.advanceTimersByTime(120)
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(2)
  })
  it('clears timeouts and intervals on unmount', () => {
    vi.useFakeTimers()
    const a = vi.fn(), b = vi.fn()
    const { getByText, unmount } = render(<Probe onTimeout={a} onInterval={b} />)
    getByText('go').click()
    unmount()
    vi.advanceTimersByTime(1000)
    expect(a).not.toHaveBeenCalled()
    expect(b).not.toHaveBeenCalled()
  })
})
