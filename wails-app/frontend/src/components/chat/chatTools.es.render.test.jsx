// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeAll, afterAll } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import i18n from '../../i18n.js'
import { ToolActivityCard } from './ToolActivityCard.jsx'
import { CoderUsageBar, CoderInitNote, initSummary } from './CoderHeader.jsx'
import { CoderBackgroundBanner, stopSummary } from './CoderBackgroundBanner.jsx'
import { computeStatusLabel } from './TurnStatus.jsx'
import { composeLiveAnnouncement } from './liveAnnouncement.js'
import { missingText, runtimeReadiness } from './useCoderMode.js'
import { takeFiles } from './chatAttachments.js'
import { ChatResetBar } from './ChatResetBar.jsx'

beforeAll(async () => { await i18n.changeLanguage('es') })
afterAll(async () => { await i18n.changeLanguage('en') })
afterEach(cleanup)

describe('chat tool cards and helpers in Spanish', () => {
  it('translates a native card and leaves the command and file text alone', () => {
    render(<ToolActivityCard call={{ callId: 'c1', native: true, kind: 'write', name: 'Write', arguments: { file_path: '/tmp/a.txt', content: 'one\ntwo' }, status: 'completed', ok: true, fileExisted: false }} />)
    expect(screen.getByText('2 líneas')).toBeTruthy()
    expect(screen.getByText('archivo nuevo')).toBeTruthy()
    expect(screen.getByText('Hecho')).toBeTruthy()
    expect(screen.getByText('/tmp/a.txt')).toBeTruthy()
  })

  it('translates a generic card', () => {
    render(<ToolActivityCard call={{ callId: 'c2', name: 'list_things', arguments: {}, status: 'completed', ok: false, result: '' }} />)
    expect(screen.getByText('Falló')).toBeTruthy()
    expect(screen.getByText('(resultado vacío)')).toBeTruthy()
    expect(screen.getByLabelText('Copiar resultado')).toBeTruthy()
  })

  it('translates the header bars and the background banner', () => {
    render(<>
      <CoderUsageBar states={[{ coderUsage: { peakContextTokens: 1000, models: {} } }]} model="" />
      <CoderInitNote workspace={{ path: '/w', init: { created: ['CLAUDE.md', 'x'] }, git: true }} />
      <CoderBackgroundBanner notice={{ processes: [{ pid: 7, command: 'sleep 9' }] }} />
      <ChatResetBar />
    </>)
    expect(screen.getByText('Contexto')).toBeTruthy()
    expect(screen.getByText('1 proceso iniciado durante este turno sigue en ejecución:')).toBeTruthy()
    expect(screen.getByText('sleep 9')).toBeTruthy()
    expect(screen.getByTestId('coder-init-note').textContent).toContain('Trabajando en /w · se creó: CLAUDE.md y 1 más · repositorio git')
    expect(screen.getByText('Compactar')).toBeTruthy()
  })

  it('translates the words built outside a component, at call time', async () => {
    expect(initSummary(['CLAUDE.md', 'a', 'b'])).toBe('CLAUDE.md y 2 más')
    expect(stopSummary({ stopped: [1, 2] })).toBe('2 procesos detenidos')
    expect(computeStatusLabel({ calls: { a: { status: 'started', name: 'Bash' } } }, 0).label).toBe('Ejecutando Bash')
    expect(composeLiveAnnouncement({ terminal: { status: 'completed' }, calls: { a: { status: 'completed', ok: false } } }))
      .toBe('Estado de la respuesta: completado. Falló 1 llamada a herramienta.')
    expect(missingText({})).toBe('necesita actualizar monomind')
    expect(runtimeReadiness({ ready: true })).toBe('listo')
    const { skipped } = await takeFiles(10, [{ name: 'a.png', size: 1 }])
    expect(skipped).toEqual(['a.png: máximo 10 archivos por mensaje'])
    await i18n.changeLanguage('en')
    expect(runtimeReadiness({ ready: true })).toBe('ready')
    await i18n.changeLanguage('es')
  })
})
