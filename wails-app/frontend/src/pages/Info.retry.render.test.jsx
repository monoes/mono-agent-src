// @vitest-environment jsdom
// W5 R21-29: a failed load offers a Retry button next to the error.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import '../i18n.js'
import Info from './Info.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', () => ({
  GetProfileInfoFields: vi.fn(), GetProfileInfo: vi.fn(), SetProfileInfo: vi.fn(), ClearProfileInfo: vi.fn(),
  RevealProfileInfo: vi.fn(), SetProfileInfoPhoto: vi.fn(), GetProfileInfoPhoto: vi.fn(), AIFillProfileInfo: vi.fn(), OpenAnyFilePicker: vi.fn(),
}))

afterEach(cleanup)

describe('Info load error', () => {
  it('Retry reloads and clears the error', async () => {
    WailsApp.GetProfileInfoFields
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValue({
        groups: [{ id: 'identity', label: 'Identity' }],
        fields: [{ key: 'identity.first_name', label: 'First name', group: 'identity', kind: 'text' }],
      })
    WailsApp.GetProfileInfo.mockResolvedValue({ profile: 'p', entries: [] })
    WailsApp.GetProfileInfoPhoto.mockResolvedValue('')
    render(<Info />)
    const alert = await screen.findByRole('alert', undefined, { timeout: 5000 })
    expect(alert).toHaveTextContent(/boom/)
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    await screen.findByLabelText('First name', undefined, { timeout: 5000 })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
