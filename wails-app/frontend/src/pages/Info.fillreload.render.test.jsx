// @vitest-environment jsdom
// W4 R21-29: a fill that worked must not be reported as failed when the reload after it fails.
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

describe('Info AI fill', () => {
  it('says the fill worked when only the reload fails', async () => {
    WailsApp.GetProfileInfoFields.mockResolvedValue({
      groups: [{ id: 'identity', label: 'Identity' }],
      fields: [{ key: 'identity.first_name', label: 'First name', group: 'identity', kind: 'text' }],
    })
    WailsApp.GetProfileInfo.mockResolvedValueOnce({ profile: 'p', entries: [] }).mockRejectedValue(new Error('reload down'))
    WailsApp.GetProfileInfoPhoto.mockResolvedValue('')
    WailsApp.AIFillProfileInfo.mockResolvedValue({ filled: ['identity.first_name'] })
    render(<Info />)
    await screen.findByLabelText('First name', undefined, { timeout: 5000 })
    fireEvent.click(screen.getByRole('button', { name: /^AI fill/ }))
    expect(await screen.findByText(/could not be reloaded/, undefined, { timeout: 5000 })).toBeInTheDocument()
  })
})
