import { describe, it, expect, vi } from 'vitest'
import { parseAppLink, appLinkTarget, resolveAppLink, appLinkUrlTransform } from './chatAppLinks.js'

const UUID = '3f2b8c1e-9a4d-4e7b-8c55-0a1b2c3d4e5f'

describe('parseAppLink', () => {
  it.each([
    ['monoagent://workflow/' + UUID, { kind: 'workflow', id: UUID }],
    ['monoagent://execution/' + UUID, { kind: 'execution', id: UUID }],
    ['monoagent://run/' + UUID, { kind: 'execution', id: UUID }],
    ['monoagent://org/My%20Org', { kind: 'org', id: 'My Org' }],
    ['monoagent://person/p_1', { kind: 'person', id: 'p_1' }],
    ['monoagent://task/42', { kind: 'task', id: '42' }],
    ['monoagent://document/doc-9', { kind: 'document', id: 'doc-9' }],
    ['monoagent://settings/health', { kind: 'settings', id: 'health' }],
    ['monoagent://page/tasks', { kind: 'page', id: 'tasks' }],
  ])('accepts %s', (href, want) => {
    expect(parseAppLink(href)).toMatchObject(want)
  })

  it.each([
    undefined, null, 42, '', ' monoagent://task/1', 'monoagent://task/1 ',
    'MONOAGENT://task/1', 'monoagent:task/1', 'monoagent:///task/1', 'monoagent://task',
    'monoagent://task/', 'monoagent://task/0', 'monoagent://task/-1', 'monoagent://task/1x',
    'monoagent://task/1/2', 'monoagent://task/1?x=1', 'monoagent://task/1#f',
    'monoagent://bogus/1', 'monoagent://settings/evil', 'monoagent://page/vaultSecrets',
    'monoagent://page/__proto__', 'monoagent://workflow/../x', 'monoagent://workflow/a%2Fb',
    'monoagent://org/%00', 'monoagent://org/' + 'a'.repeat(200), 'monoagent://org/%E0%A4%A',
    'monoagent://user@host/task/1', 'javascript:alert(1)', 'https://example.com',
  ])('rejects %j', (href) => {
    expect(parseAppLink(href)).toBeNull()
  })
})

describe('appLinkTarget', () => {
  it('maps each kind to navigate(page, data)', () => {
    const t = (href) => appLinkTarget(parseAppLink(href))
    expect(t('monoagent://workflow/w1')).toEqual({ page: 'noderunner', data: { workflowId: 'w1' } })
    expect(t('monoagent://run/e1')).toEqual({ page: 'noderunner', data: { executionId: 'e1' } })
    expect(t('monoagent://org/Acme')).toEqual({ page: 'orgs', data: { org: 'Acme' } })
    expect(t('monoagent://person/p1')).toEqual({ page: 'people', data: { person: 'p1' } })
    expect(t('monoagent://task/7')).toEqual({ page: 'tasks', data: null })
    expect(t('monoagent://document/d1')).toEqual({ page: 'documents', data: null })
    expect(t('monoagent://settings/jev')).toEqual({ page: 'settings', data: { section: 'jev' } })
    expect(t('monoagent://page/people')).toEqual({ page: 'people', data: null })
  })
})

describe('resolveAppLink', () => {
  const deps = () => ({
    api: {
      getWorkflow: vi.fn(async (id) => (id === 'w1' ? { id: 'w1' } : null)),
      getExecutionDetail: vi.fn(async (id) => (id === 'e1' ? { id: 'e1' } : null)),
      listOrgDesigns: vi.fn(async () => [{ name: 'Acme' }]),
      getPersonDetail: vi.fn(async (id) => (id === 'p1' ? { id: 'p1' } : null)),
      getProfileDocument: vi.fn(async (id) => (id === 'd1' ? { id: 'd1' } : null)),
    },
    tasksApi: { show: vi.fn(async (id) => (id === 7 ? { task: { id: 7 } } : { error: 'not found', code: 'not_found' })) },
  })
  const r = (href, d = deps()) => resolveAppLink(parseAppLink(href), d)

  it('resolves real targets', async () => {
    expect(await r('monoagent://workflow/w1')).toEqual({ page: 'noderunner', data: { workflowId: 'w1' } })
    expect(await r('monoagent://run/e1')).toEqual({ page: 'noderunner', data: { executionId: 'e1' } })
    expect(await r('monoagent://org/Acme')).toEqual({ page: 'orgs', data: { org: 'Acme' } })
    expect(await r('monoagent://person/p1')).toEqual({ page: 'people', data: { person: 'p1' } })
    expect(await r('monoagent://task/7')).toEqual({ page: 'tasks', data: null })
    expect(await r('monoagent://document/d1')).toEqual({ page: 'documents', data: null })
  })

  it('resolves forged or missing ids to null', async () => {
    for (const h of ['workflow/nope', 'run/nope', 'org/Nope', 'person/nope', 'task/8', 'document/nope']) {
      expect(await r('monoagent://' + h)).toBeNull()
    }
  })

  it('needs no lookup for settings and page links', async () => {
    const d = deps()
    expect(await r('monoagent://settings/health', d)).toEqual({ page: 'settings', data: { section: 'health' } })
    expect(await r('monoagent://page/orgs', d)).toEqual({ page: 'orgs', data: null })
    expect(Object.values(d.api).every(f => f.mock.calls.length === 0)).toBe(true)
  })

  it('is null when the lookup throws or the binding is missing', async () => {
    const d = deps()
    d.api.getWorkflow = vi.fn(async () => { throw new Error('boom') })
    expect(await r('monoagent://workflow/w1', d)).toBeNull()
    expect(await r('monoagent://person/p1', { api: {}, tasksApi: {} })).toBeNull()
  })

  it('rejects a backend answer for a different id', async () => {
    const d = deps()
    d.api.getWorkflow = vi.fn(async () => ({ id: 'other' }))
    expect(await r('monoagent://workflow/w1', d)).toBeNull()
  })

  it('null link is null', async () => {
    expect(await resolveAppLink(null, deps())).toBeNull()
  })
})

describe('appLinkUrlTransform', () => {
  it('keeps a valid app link, defers everything else to the default', () => {
    expect(appLinkUrlTransform('monoagent://task/3')).toBe('monoagent://task/3')
    expect(appLinkUrlTransform('monoagent://task/x')).toBe('')
    expect(appLinkUrlTransform('javascript:alert(1)')).toBe('')
    expect(appLinkUrlTransform('https://example.com/a')).toBe('https://example.com/a')
  })
})
