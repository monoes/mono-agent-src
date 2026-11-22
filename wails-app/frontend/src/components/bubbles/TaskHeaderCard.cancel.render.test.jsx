// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { aitaskApi } = vi.hoisted(() => ({ aitaskApi: { get: vi.fn(), cancel: vi.fn() } }))
vi.mock('../SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi }))
vi.mock('../settings/aitaskBudgetApi.js', () => ({ aitaskBudgetApi: { forTask: vi.fn(() => Promise.resolve(null)) } }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))

import { confirm } from '../ConfirmDialog.jsx'
import { TaskHeaderCard } from './TaskHeaderCard.jsx'

afterEach(() => { cleanup(); vi.clearAllMocks() })

const task = { taskId: 't1', goal: 'goal', status: 'running' }

describe('TaskHeaderCard cancel', () => {
  it('asks before cancelling a running task and does nothing when the answer is no', async () => {
    aitaskApi.get.mockResolvedValue({ status: 'running' })
    confirm.mockResolvedValue(false)
    render(<TaskHeaderCard task={task} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel task' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][1]).toMatchObject({ danger: true, cancelLabel: 'Keep running' })
    expect(aitaskApi.cancel).not.toHaveBeenCalled()
  })

  it('cancels once the answer is yes', async () => {
    aitaskApi.get.mockResolvedValue({ status: 'running' })
    confirm.mockResolvedValue(true)
    aitaskApi.cancel.mockResolvedValue({ status: 'cancelled' })
    render(<TaskHeaderCard task={task} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel task' }))
    await waitFor(() => expect(aitaskApi.cancel).toHaveBeenCalledWith('t1'))
  })
})
