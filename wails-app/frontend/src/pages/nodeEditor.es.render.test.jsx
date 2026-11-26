// @vitest-environment jsdom
// The workflow editor in Spanish: schema-driven step settings, the form builder and the editor's
// own messages. A text with no catalog entry stays as the schema wrote it.
import { describe, it, expect, vi, afterEach, beforeAll, afterAll } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../i18n.js'
import catalog from '../locales/schema/es.json'
import { localizeFields, localizeSubInputs } from '../lib/schemaI18n.js'
import { Inspector } from './NodeRunner.jsx'
import FormFieldsEditor from './FormFieldsEditor.jsx'
import { checkSubConnection } from './aiSubNodes.js'
import { parseTriggerInput } from './triggerInput.js'

vi.mock('../wailsjs/go/main/App', async (importOriginal) => ({
  ...await importOriginal(),
  GetVaultImages: vi.fn(() => Promise.resolve([])),
}))
vi.mock('../services/api.js', () => ({
  api: {}, notify: vi.fn(), reportRunFailure: vi.fn(), subscribeEvent: vi.fn(() => () => {}),
}))

afterEach(cleanup)
beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))

const node = {
  id: 'n1', subtype: 'trigger.schedule', label: 'Schedule', category: 'triggers', config: {},
  schema: {
    fields: [
      { key: 'cron', label: 'Cron Expression', type: 'text', help: 'six fields' },
      { key: 'not_in_catalog', label: 'Brand New Setting', type: 'text', help: 'Not translated yet.' },
    ],
  },
}
const inspector = () => render(
  <Inspector node={node} onConfigChange={vi.fn()} onClose={vi.fn()} liveSchemas={{}} automationIds={new Set()} />,
)

describe('step settings in Spanish', () => {
  it('shows the catalog text and keeps English for a field without an entry', async () => {
    inspector()
    expect(await screen.findByText(catalog['trigger.schedule'].cron.label)).toBeInTheDocument()
    expect(screen.getByText(catalog['trigger.schedule'].cron.help)).toBeInTheDocument()
    expect(screen.getByText('Brand New Setting')).toBeInTheDocument()
    expect(screen.getByText('Not translated yet.')).toBeInTheDocument()
  })

  it('shows the English schema text in English', async () => {
    await i18n.changeLanguage('en')
    try {
      inspector()
      expect(await screen.findByText('Cron Expression')).toBeInTheDocument()
      expect(screen.getByText('six fields')).toBeInTheDocument()
    } finally {
      await i18n.changeLanguage('es')
    }
  })

  it('translates only texts the schema has, and returns the fields untouched without an entry', () => {
    const fields = [{ key: 'cron', label: 'Cron Expression' }]
    const [cron] = localizeFields(catalog, 'trigger.schedule', fields)
    expect(cron.label).toBe(catalog['trigger.schedule'].cron.label)
    expect(cron).not.toHaveProperty('help')
    expect(localizeFields(catalog, 'no.such_node', fields)).toBe(fields)
    expect(localizeFields(null, 'trigger.schedule', fields)).toBe(fields)
  })

  it('translates sub-node port labels and the messages that name them', () => {
    const target = localizeSubInputs(catalog, {
      id: 't', label: 'Agente', subtype: 'agent.ask', subInputs: [{ handle: 'ai_languageModel', label: 'Chat Model', max: 1 }],
    })
    expect(target.subInputs[0].label).toBe(catalog['agent.ask']['@ai_languageModel'].label)
    const check = checkSubConnection({ id: 's', label: 'Otro', subOutput: 'ai_tool' }, target, 'ai_languageModel', [])
    expect(check.message).toMatch(/^Conexión rechazada: Otro/)
    expect(check.message).toContain(target.subInputs[0].label)
  })

  it('shows the form builder and the trigger input errors in Spanish', () => {
    render(<FormFieldsEditor field={{ key: 'formFields', label: 'Campos' }} value="" config={{}} onCommit={vi.fn()} inputStyle={{}} />)
    expect(screen.getByRole('button', { name: /Añadir campo/ })).toBeInTheDocument()
    expect(parseTriggerInput('[').error).toMatch(/^JSON no válido/)
  })
})
