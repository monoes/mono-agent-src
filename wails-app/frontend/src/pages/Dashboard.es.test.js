import { describe, it, expect, vi, beforeAll, afterAll } from 'vitest'
import i18n from '../i18n.js'
import { execStatusWord } from './dashboard/format.js'
import { relativeTime, orgRunLabel, survivorNames } from '../lib/historyModel.js'
import { errorText } from '../components/info/infoModel.js'
import { onApiError, api, reportRunFailure } from '../services/api.js'
import { resolve } from '../services/hilApi.js'
import { answerOf } from '../services/account.js'

vi.mock('../wailsjs/go/main/App', () => ({ GetLogs: vi.fn(() => Promise.reject(new Error('boom'))) }))

beforeAll(() => i18n.changeLanguage('es'))
afterAll(() => i18n.changeLanguage('en'))

describe('dashboard, history and service words in Spanish', () => {
  it('translates execution statuses and keeps unknown ones as stored', () => {
    expect(execStatusWord('SUCCESS_WITH_ERRORS')).toBe('Correcta con errores')
    expect(execStatusWord('SOMETHING_NEW')).toBe('SOMETHING_NEW')
  })

  it('translates the history rows', () => {
    const now = Date.parse('2026-01-02T00:00:00Z')
    expect(relativeTime('2026-01-01T22:00:00Z', now)).toBe('hace 2 h')
    expect(orgRunLabel({ runId: 'abc' })).toBe('ejecución abc')
    expect(survivorNames([{ pid: 7 }])).toBe('proceso (7)')
    expect(errorText('')).toBe('error desconocido')
  })

  it('names failed operations and bridge failures in Spanish', async () => {
    const seen = []
    const off = onApiError(d => seen.push(d))
    await api.getLogs()
    reportRunFailure('workflow wf is inactive', { activateAndRun: () => {} })
    off()
    expect(seen[0].op).toBe('registros')
    expect(seen[1].op).toBe('ejecutar flujo de trabajo')
    expect(await resolve('', {})).toEqual({ error: 'falta el identificador' })
    expect(answerOf('not json').failure.message).toBe('monoagentcli respondió con algo ilegible')
  })
})
