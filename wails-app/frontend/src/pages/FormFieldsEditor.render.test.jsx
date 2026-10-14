// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import FormFieldsEditor from './FormFieldsEditor.jsx'

afterEach(cleanup)

const field = { key: 'formFields', label: 'Form Fields (JSON)', type: 'code', language: 'json', required: true }
const setup = (value, config = {}) => {
  const onCommit = vi.fn()
  render(<FormFieldsEditor field={field} value={value} config={config} onCommit={onCommit} inputStyle={{}} />)
  return onCommit
}
const type = (label, value) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

describe('FormFieldsEditor', () => {
  it('builds a form from nothing and saves only once it is valid', () => {
    const onCommit = setup('')
    expect(screen.getByText(/No fields yet/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Add field/ }))
    expect(screen.getByLabelText('Field 1 label')).toHaveFocus()
    expect(screen.getByRole('alert')).toHaveTextContent('Give the field a label.')
    expect(onCommit).not.toHaveBeenCalled()

    type('Field 1 label', 'Name')
    fireEvent.click(within(screen.getByRole('listitem')).getByLabelText('Required'))
    expect(onCommit).toHaveBeenLastCalledWith([{ fieldLabel: 'Name', fieldType: 'text', requiredField: true }])

    fireEvent.click(screen.getByRole('button', { name: /Add field/ }))
    type('Field 2 label', 'Topic')
    type('Field 2 type', 'dropdown')
    expect(screen.getByRole('alert')).toHaveTextContent('Add at least one option.')
    const saves = onCommit.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: /Add option/ }))
    type('Field 2 option 1', 'Sales')
    fireEvent.click(screen.getByRole('button', { name: /Add option/ }))
    type('Field 2 option 2', 'Support')
    expect(onCommit.mock.calls.length).toBeGreaterThan(saves)
    expect(onCommit).toHaveBeenLastCalledWith([
      { fieldLabel: 'Name', fieldType: 'text', requiredField: true },
      { fieldLabel: 'Topic', fieldType: 'dropdown', fieldOptions: { values: [{ option: 'Sales' }, { option: 'Support' }] } },
    ])
  })

  it('reorders and removes fields and options', () => {
    const onCommit = setup([
      { fieldLabel: 'A' },
      { fieldLabel: 'B', fieldType: 'dropdown', fieldOptions: ['x', 'y'] },
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Move option 2 up' }))
    expect(onCommit).toHaveBeenLastCalledWith([
      { fieldLabel: 'A', fieldType: 'text' },
      { fieldLabel: 'B', fieldType: 'dropdown', fieldOptions: { values: [{ option: 'y' }, { option: 'x' }] } },
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Move field 2 up' }))
    expect(onCommit.mock.lastCall[0].map(f => f.fieldLabel)).toEqual(['B', 'A'])
    expect(screen.getByRole('button', { name: 'Move field 1 up' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Remove field 1' }))
    expect(onCommit).toHaveBeenLastCalledWith([{ fieldLabel: 'A', fieldType: 'text' }])
  })

  it('keeps unknown keys through edits', () => {
    const onCommit = setup({ values: [{ fieldLabel: 'Pick', fieldType: 'text', fieldName: 'pick', elementName: 'e1' }] })
    type('Field 1 label', 'Picked')
    expect(onCommit).toHaveBeenLastCalledWith([{ fieldLabel: 'Picked', fieldType: 'text', fieldName: 'pick', elementName: 'e1' }])
  })

  it('previews the form with the node title, description and button', () => {
    setup([
      { fieldLabel: 'Email', fieldType: 'email', requiredField: true, placeholder: 'you@example.com' },
      { fieldLabel: 'Plan', fieldType: 'dropdown', fieldOptions: ['Free', 'Pro'] },
      { fieldLabel: 'Agree', fieldType: 'checkbox' },
    ], { formTitle: 'Sign up', formDescription: 'Takes a minute.', buttonLabel: 'Join' })
    const preview = within(screen.getByRole('region', { name: 'Form preview' }))
    expect(preview.getByRole('heading', { name: 'Sign up' })).toBeInTheDocument()
    expect(preview.getByText('Takes a minute.')).toBeInTheDocument()
    expect(preview.getByPlaceholderText('you@example.com')).toHaveAttribute('type', 'email')
    expect(preview.getByRole('option', { name: 'Pro' })).toBeInTheDocument()
    expect(preview.getByRole('checkbox')).toBeInTheDocument()
    expect(preview.getByRole('button', { name: 'Join' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Hide preview' }))
    expect(screen.queryByRole('region', { name: 'Form preview' })).toBeNull()
  })

  it('switches to raw JSON and back, keeping edits made there', () => {
    const onCommit = setup([{ fieldLabel: 'A' }])
    fireEvent.click(screen.getByRole('button', { name: 'Raw JSON' }))
    const box = screen.getByLabelText('Form Fields (JSON) JSON')
    expect(JSON.parse(box.value)).toEqual([{ fieldLabel: 'A', fieldType: 'text' }])

    fireEvent.change(box, { target: { value: '[{"label": "B"}' } })
    expect(onCommit).toHaveBeenLastCalledWith('[{"label": "B"}')
    fireEvent.click(screen.getByRole('button', { name: 'Visual editor' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Not valid JSON')
    expect(screen.getByLabelText('Form Fields (JSON) JSON')).toBeInTheDocument()

    fireEvent.change(box, { target: { value: '[{"label": "B"}]' } })
    fireEvent.click(screen.getByRole('button', { name: 'Visual editor' }))
    expect(screen.getByLabelText('Field 1 label')).toHaveValue('B')
  })

  it('opens in raw JSON when the stored value cannot be shown as cards', () => {
    const onCommit = setup('{{ $json.fields }}')
    expect(screen.getByLabelText('Form Fields (JSON) JSON')).toHaveValue('{{ $json.fields }}')
    expect(screen.getByRole('alert')).toHaveTextContent(/template/)
    expect(onCommit).not.toHaveBeenCalled()
  })
})
