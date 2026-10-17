// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import '../i18n.js'
import Info from './Info.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', () => ({
  GetProfileInfoFields: vi.fn(),
  GetProfileInfo: vi.fn(),
  SetProfileInfo: vi.fn(),
  ClearProfileInfo: vi.fn(),
  RevealProfileInfo: vi.fn(),
  SetProfileInfoPhoto: vi.fn(),
  GetProfileInfoPhoto: vi.fn(),
  AIFillProfileInfo: vi.fn(),
  OpenAnyFilePicker: vi.fn(),
}))

const catalog = {
  groups: [{ id: 'identity', label: 'Identity' }, { id: 'ids', label: 'IDs' }, { id: 'about', label: 'About' }, { id: 'photos', label: 'Photos' }, { id: 'card', label: 'Card' }],
  fields: [
    { key: 'identity.first_name', label: 'First name', group: 'identity', kind: 'text' },
    { key: 'identity.gender', label: 'Gender', group: 'identity', kind: 'select', options: ['female', 'male'] },
    { key: 'ids.passport', label: 'Passport number', group: 'ids', kind: 'text', sensitive: true },
    { key: 'about.bio', label: 'Bio', group: 'about', kind: 'textarea' },
    { key: 'photos.portrait', label: 'Portrait', group: 'photos', kind: 'photo' },
    { key: 'card.number', label: 'Card number', group: 'card', kind: 'text', card: true },
  ],
}
const aiName = { key: 'identity.first_name', value: 'Ada', masked: false, source: 'ai', origin: 'cv.pdf', updated_at: 't' }
const passport = { key: 'ids.passport', value: '•••• 4567', masked: true, source: 'user', updated_at: 't' }

let entries
beforeEach(() => {
  vi.clearAllMocks()
  entries = [aiName, passport]
  WailsApp.GetProfileInfoFields.mockResolvedValue(catalog)
  WailsApp.GetProfileInfo.mockImplementation(() => Promise.resolve({ profile: 'p', entries }))
  WailsApp.SetProfileInfo.mockResolvedValue(undefined)
  WailsApp.ClearProfileInfo.mockResolvedValue(undefined)
  WailsApp.GetProfileInfoPhoto.mockResolvedValue('')
})
afterEach(() => { cleanup(); vi.useRealTimers() })

async function loaded() {
  render(<Info />)
  await screen.findByLabelText('First name')
}

describe('Info page', () => {
  it('renders sections and fields from the catalog by kind', async () => {
    await loaded()
    expect(screen.getByRole('button', { name: /Identity/ })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByLabelText('First name').tagName).toBe('INPUT')
    expect(screen.getByLabelText('First name')).toHaveValue('Ada')
    expect(screen.getByLabelText('Gender').tagName).toBe('SELECT')
    expect(screen.getByLabelText('Bio').tagName).toBe('TEXTAREA')
    expect(screen.getByText(/Details MonoAgent fills into websites/)).toBeInTheDocument()
  })

  it('collapses and expands a section', async () => {
    await loaded()
    const head = screen.getByRole('button', { name: /Identity/ })
    fireEvent.click(head)
    expect(head).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByLabelText('First name')).not.toBeVisible()
    fireEvent.click(head)
    expect(screen.getByLabelText('First name')).toBeVisible()
  })

  it('autosaves a textarea through the binding on blur, keeping newlines', async () => {
    await loaded()
    const bio = screen.getByLabelText('Bio')
    fireEvent.change(bio, { target: { value: 'line 1\nline 2' } })
    fireEvent.blur(bio)
    await waitFor(() => expect(WailsApp.SetProfileInfo).toHaveBeenCalledWith('about.bio', 'line 1\nline 2'))
    expect(await screen.findByText('Saved')).toBeInTheDocument()
  })

  it('does not save an unchanged field', async () => {
    await loaded()
    fireEvent.blur(screen.getByLabelText('First name'))
    expect(WailsApp.SetProfileInfo).not.toHaveBeenCalled()
  })

  it('saves a select on change', async () => {
    await loaded()
    fireEvent.change(screen.getByLabelText('Gender'), { target: { value: 'female' } })
    await waitFor(() => expect(WailsApp.SetProfileInfo).toHaveBeenCalledWith('identity.gender', 'female'))
  })

  it('shows the CLI validation message when a save fails', async () => {
    WailsApp.SetProfileInfo.mockRejectedValue(new Error('invalid value for identity.first_name'))
    await loaded()
    const f = screen.getByLabelText('First name')
    fireEvent.change(f, { target: { value: 'x' } })
    fireEvent.blur(f)
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid value for identity.first_name')
  })

  it('marks AI values and makes an edited one the user\'s', async () => {
    await loaded()
    expect(screen.getByLabelText('Filled by AI from cv.pdf')).toHaveTextContent('AI')
    const f = screen.getByLabelText('First name')
    fireEvent.change(f, { target: { value: 'Grace' } })
    fireEvent.blur(f)
    await waitFor(() => expect(screen.queryByLabelText('Filled by AI from cv.pdf')).not.toBeInTheDocument())
  })
})

describe('Info masked fields', () => {
  it('shows the masked value read-only and Change opens an empty input that saves', async () => {
    await loaded()
    const shown = screen.getByLabelText('Passport number')
    expect(shown).toHaveValue('•••• 4567')
    expect(shown).toHaveAttribute('readonly')
    fireEvent.click(screen.getByRole('button', { name: 'Change: Passport number' }))
    const input = screen.getByLabelText('Passport number')
    expect(input).toHaveValue('')
    fireEvent.change(input, { target: { value: 'P998877' } })
    entries = [aiName, { ...passport, value: '•••• 8877' }]
    fireEvent.blur(input)
    await waitFor(() => expect(WailsApp.SetProfileInfo).toHaveBeenCalledWith('ids.passport', 'P998877'))
    await waitFor(() => expect(screen.getByLabelText('Passport number')).toHaveValue('•••• 8877'))
  })

  it('Change then an empty blur cancels without clearing', async () => {
    await loaded()
    fireEvent.click(screen.getByRole('button', { name: 'Change: Passport number' }))
    fireEvent.blur(screen.getByLabelText('Passport number'))
    expect(WailsApp.SetProfileInfo).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Passport number')).toHaveValue('•••• 4567')
  })

  it('the eye shows the value, the same button hides it again, and it hides itself after a minute', async () => {
    WailsApp.RevealProfileInfo.mockResolvedValue('P1234567')
    await loaded()
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fireEvent.click(screen.getByRole('button', { name: 'Reveal: Passport number' }))
    await waitFor(() => expect(screen.getByLabelText('Passport number')).toHaveValue('P1234567'))
    expect(WailsApp.RevealProfileInfo).toHaveBeenCalledWith('ids.passport')
    expect(screen.getByLabelText('Passport number')).not.toHaveClass('is-blurred')
    fireEvent.click(screen.getByRole('button', { name: 'Hide: Passport number' }))
    expect(screen.getByLabelText('Passport number')).toHaveValue('•••• 4567')
    expect(screen.getByLabelText('Passport number')).toHaveClass('is-blurred')
    fireEvent.click(screen.getByRole('button', { name: 'Reveal: Passport number' }))
    await waitFor(() => expect(screen.getByLabelText('Passport number')).toHaveValue('P1234567'))
    await act(async () => { vi.advanceTimersByTime(59000) })
    expect(screen.getByLabelText('Passport number')).toHaveValue('P1234567')
    await act(async () => { vi.advanceTimersByTime(1100) })
    expect(screen.getByLabelText('Passport number')).toHaveValue('•••• 4567')
  })

  it('explains how the card is stored and offers no AI for it', async () => {
    await loaded()
    expect(screen.getByText(/Stored encrypted on this computer\. Never sent to an AI\./)).toBeInTheDocument()
  })
})

describe('Info AI fill', () => {
  it('is busy while running, then summarises and refreshes the entries', async () => {
    let done
    WailsApp.AIFillProfileInfo.mockReturnValue(new Promise(r => { done = r }))
    await loaded()
    const btn = screen.getByRole('button', { name: 'AI fill' })
    fireEvent.click(btn)
    const busy = await screen.findByRole('button', { name: /Reading your documents/ })
    expect(busy).toBeDisabled()
    entries = [aiName, passport, { key: 'about.bio', value: 'Engineer', masked: false, source: 'ai', origin: 'cv.pdf', updated_at: 't' }]
    await act(async () => { done({ asked: 4, filled: [{ key: 'about.bio', origin: 'cv.pdf' }], skipped: 0 }) })
    expect(await screen.findByText(/Filled 1 field from your documents, 1 left empty/)).toBeInTheDocument()
    expect(screen.getByLabelText('Bio')).toHaveValue('Engineer')
    expect(screen.getByRole('button', { name: 'AI fill' })).toBeEnabled()
    expect(WailsApp.AIFillProfileInfo).toHaveBeenCalledWith('', '')
  })

  it('shows a readable message when the AI cannot run', async () => {
    WailsApp.AIFillProfileInfo.mockRejectedValue(new Error('no AI runtime is set up'))
    await loaded()
    fireEvent.click(screen.getByRole('button', { name: 'AI fill' }))
    expect(await screen.findByText(/AI fill did not finish: no AI runtime is set up/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'AI fill' })).toBeEnabled()
  })
})

describe('Info photo slot', () => {
  it('uploads through the file picker and the binding', async () => {
    WailsApp.OpenAnyFilePicker.mockResolvedValue('/tmp/me.png')
    WailsApp.SetProfileInfoPhoto.mockResolvedValue(undefined)
    WailsApp.GetProfileInfoPhoto.mockResolvedValue('data:image/png;base64,AAAA')
    await loaded()
    expect(screen.getByText('No file')).toBeInTheDocument()
    entries = [...entries, { key: 'photos.portrait', value: 'portrait.png', masked: false, source: 'user', updated_at: 't' }]
    fireEvent.click(screen.getByRole('button', { name: 'Upload: Portrait' }))
    await waitFor(() => expect(WailsApp.SetProfileInfoPhoto).toHaveBeenCalledWith('photos.portrait', '/tmp/me.png'))
    const img = await screen.findByAltText('Portrait')
    expect(img).toHaveAttribute('src', 'data:image/png;base64,AAAA')
  })

  it('shows a file chip for a PDF and removes through ClearProfileInfo', async () => {
    entries = [...entries, { key: 'photos.portrait', value: 'scan.pdf', masked: false, source: 'user', updated_at: 't' }]
    WailsApp.GetProfileInfoPhoto.mockResolvedValue('data:application/pdf;base64,AAAA')
    await loaded()
    expect(await screen.findByText('PDF')).toBeInTheDocument()
    expect(screen.queryByAltText('Portrait')).not.toBeInTheDocument()
    entries = entries.filter(e => e.key !== 'photos.portrait')
    fireEvent.click(screen.getByRole('button', { name: 'Remove: Portrait' }))
    await waitFor(() => expect(WailsApp.ClearProfileInfo).toHaveBeenCalledWith('photos.portrait'))
    await waitFor(() => expect(screen.getByText('No file')).toBeInTheDocument())
  })
})

describe('Info page: person or business', () => {
  const scoped = {
    groups: [
      { id: 'type', label: 'Profile type' },
      { id: 'identity', label: 'Identity', scope: 'person' },
      { id: 'biz_tax', label: 'Tax & registration', scope: 'business' },
      { id: 'financial', label: 'Bank account' },
    ],
    fields: [
      { key: 'profile_type', label: 'Profile type', group: 'type', kind: 'select', options: ['Person', 'Business'] },
      { key: 'first_name', label: 'First name', group: 'identity', kind: 'text' },
      { key: 'vat_id', label: 'VAT ID', group: 'biz_tax', kind: 'text' },
      { key: 'iban', label: 'IBAN', group: 'financial', kind: 'text' },
    ],
  }
  beforeEach(() => {
    entries = []
    WailsApp.GetProfileInfoFields.mockResolvedValue(scoped)
    WailsApp.SetProfileInfo.mockImplementation((key, value) => {
      entries = [{ key, value, masked: false, source: 'user', updated_at: 't' }]
      return Promise.resolve()
    })
  })

  it('starts as a person, hides the type field and the business sections', async () => {
    render(<Info />)
    await screen.findByLabelText('First name')
    expect(screen.getByLabelText('IBAN')).toBeInTheDocument()
    expect(screen.queryByLabelText('VAT ID')).not.toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: 'Profile type' })).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Person' })).toHaveAttribute('aria-checked', 'true')
  })

  it('switches to business: VAT shows, personal sections go, shared ones stay', async () => {
    render(<Info />)
    await screen.findByLabelText('First name')
    fireEvent.click(screen.getByRole('radio', { name: 'Business' }))
    await waitFor(() => expect(WailsApp.SetProfileInfo).toHaveBeenCalledWith('profile_type', 'Business'))
    expect(await screen.findByLabelText('VAT ID')).toBeInTheDocument()
    expect(screen.queryByLabelText('First name')).not.toBeInTheDocument()
    expect(screen.getByLabelText('IBAN')).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Business' })).toHaveAttribute('aria-checked', 'true')
  })
})
