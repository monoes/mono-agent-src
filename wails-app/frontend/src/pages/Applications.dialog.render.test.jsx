// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import Applications from './Applications.jsx'
import ApplicationsProcessPendingFlow from './ApplicationsProcessPendingFlow.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', () => ({
  GetApplications: vi.fn(() => Promise.resolve([])),
  GetApplication: vi.fn(), GetApplicationAssets: vi.fn(), AddApplication: vi.fn(),
  SetApplicationStatus: vi.fn(), OpenURL: vi.fn(), OpenPathWithOS: vi.fn(),
  OpenJSONFilePicker: vi.fn(), ReadJSONFile: vi.fn(), EvaluateApplication: vi.fn(),
  HasGeneratedDocuments: vi.fn(), ApplyToApplication: vi.fn(), SendApplication: vi.fn(),
}))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))

afterEach(cleanup)

describe('Applications overlays are dialogs', () => {
  it('Add Application is a labelled dialog that closes on Escape and labels its inputs', async () => {
    render(<Applications />)
    await waitFor(() => expect(WailsApp.GetApplications).toHaveBeenCalled())
    fireEvent.click(screen.getByRole('button', { name: /Add/i }))
    const dlg = screen.getByRole('dialog', { name: 'Add Application' })
    expect(dlg).toHaveAttribute('aria-modal', 'true')
    expect(screen.getByLabelText('Title')).toBeInTheDocument()
    expect(screen.getByLabelText('URL')).toBeInTheDocument()
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('Process Pending is a dialog that closes on Escape', () => {
    const onClose = vi.fn()
    render(<ApplicationsProcessPendingFlow pendingApplications={[]} onClose={onClose} />)
    const dlg = screen.getByRole('dialog', { name: 'Process Pending Applications' })
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})
