import { describe, it, expect } from 'vitest'
import { parseFormFields, serializeFormFields, validateFormFields, newField, moveItem } from './formFieldsModel.js'
import { isFormFieldsField } from './nodeInspectorRules.js'

const roundTrip = v => {
  const r = parseFormFields(v)
  expect(r.error).toBeUndefined()
  return serializeFormFields(r.fields)
}

const canonical = [
  { fieldLabel: 'Name', fieldType: 'text', requiredField: true, placeholder: 'Ada' },
  { fieldLabel: 'Email', fieldType: 'email' },
  { fieldLabel: 'Topic', fieldType: 'dropdown', fieldOptions: { values: [{ option: 'Sales' }, { option: 'Support' }] } },
  { fieldLabel: 'Agree', fieldType: 'checkbox', requiredField: false },
]

describe('parseFormFields / serializeFormFields', () => {
  it('writes canonical input back unchanged', () => {
    expect(roundTrip(canonical)).toEqual(canonical)
  })
  it('reads the n8n {values: [...]} wrapper', () => {
    expect(roundTrip({ values: canonical })).toEqual(canonical)
  })
  it('reads JSON text, of the list and of the wrapper', () => {
    expect(roundTrip(JSON.stringify(canonical))).toEqual(canonical)
    expect(roundTrip(JSON.stringify({ values: canonical }, null, 2))).toEqual(canonical)
  })
  it('maps label/type/options/required aliases to n8n keys', () => {
    expect(roundTrip([{ label: 'Plan', type: 'dropdown', options: ['A', 'B'], required: true }])).toEqual([
      { fieldLabel: 'Plan', fieldType: 'dropdown', requiredField: true, fieldOptions: { values: [{ option: 'A' }, { option: 'B' }] } },
    ])
  })
  it('reads options as a list, a comma string or n8n values', () => {
    const want = { values: [{ option: 'x' }, { option: 'y' }] }
    expect(roundTrip([{ fieldLabel: 'a', fieldType: 'dropdown', fieldOptions: ['x', 'y'] }])[0].fieldOptions).toEqual(want)
    expect(roundTrip([{ fieldLabel: 'a', fieldType: 'dropdown', fieldOptions: 'x, y,' }])[0].fieldOptions).toEqual(want)
    expect(roundTrip([{ fieldLabel: 'a', fieldType: 'dropdown', fieldOptions: want }])[0].fieldOptions).toEqual(want)
  })
  it('keeps keys it does not know, on fields and on options', () => {
    const v = [{
      fieldLabel: 'Pick', fieldType: 'dropdown', fieldName: 'pick', multiselect: true, elementName: 'x',
      fieldOptions: { values: [{ option: 'One', extra: 1 }] },
    }]
    expect(roundTrip(v)).toEqual(v)
  })
  it('defaults a missing type to text and does not invent optional keys', () => {
    expect(roundTrip([{ fieldLabel: 'Name' }])).toEqual([{ fieldLabel: 'Name', fieldType: 'text' }])
  })
  it('keeps a field type it does not list', () => {
    expect(roundTrip([{ fieldLabel: 'Secret', fieldType: 'password' }])).toEqual([{ fieldLabel: 'Secret', fieldType: 'password' }])
  })
  it('treats empty values as no fields', () => {
    for (const v of [undefined, null, '', '  ', [], { values: [] }]) expect(parseFormFields(v)).toEqual({ fields: [] })
  })
  it('reports what it cannot show', () => {
    expect(parseFormFields('[{').error).toMatch(/Not valid JSON/)
    expect(parseFormFields('{{ $json.fields }}').error).toMatch(/template/)
    expect(parseFormFields(['x']).error).toMatch(/Field 1 is not an object/)
    expect(parseFormFields(42).error).toMatch(/list/)
  })
  it('drops blank options and trims labels on save', () => {
    const r = parseFormFields([{ fieldLabel: ' Topic ', fieldType: 'dropdown', fieldOptions: ['A'] }])
    r.fields[0].options.push({ option: '  ' })
    expect(serializeFormFields(r.fields)).toEqual([
      { fieldLabel: 'Topic', fieldType: 'dropdown', fieldOptions: { values: [{ option: 'A' }] } },
    ])
  })
})

describe('validateFormFields', () => {
  const fieldsOf = v => parseFormFields(v).fields
  it('flags an empty label', () => {
    const f = fieldsOf([{ fieldLabel: '' }, { fieldLabel: 'ok' }])
    expect(validateFormFields(f)).toEqual({ [f[0].id]: 'Give the field a label.' })
  })
  it('flags a dropdown without options, blank ones included', () => {
    const f = fieldsOf([{ fieldLabel: 'T', fieldType: 'dropdown', fieldOptions: [' '] }])
    expect(validateFormFields(f)[f[0].id]).toBe('Add at least one option.')
  })
  it('allows a checkbox without options', () => {
    expect(validateFormFields(fieldsOf([{ fieldLabel: 'Agree', fieldType: 'checkbox' }]))).toEqual({})
  })
  it('flags a label used twice', () => {
    const f = fieldsOf([{ fieldLabel: 'A' }, { fieldLabel: ' A ' }])
    expect(validateFormFields(f)[f[1].id]).toMatch(/already labelled "A"/)
  })
  it('flags a new field until it has a label', () => {
    expect(Object.keys(validateFormFields([newField()]))).toHaveLength(1)
  })
})

describe('moveItem', () => {
  it('moves within bounds and ignores moves past the ends', () => {
    expect(moveItem([1, 2, 3], 0, 1)).toEqual([2, 1, 3])
    expect(moveItem([1, 2, 3], 2, -1)).toEqual([1, 3, 2])
    const l = [1, 2]
    expect(moveItem(l, 0, -1)).toBe(l)
    expect(moveItem(l, 1, 1)).toBe(l)
  })
})

describe('isFormFieldsField', () => {
  it('matches the formFields key on any node', () => {
    expect(isFormFieldsField({ key: 'formFields', type: 'code' })).toBe(true)
    expect(isFormFieldsField({ key: 'formTitle' })).toBe(false)
    expect(isFormFieldsField(undefined)).toBe(false)
  })
})
