import { describe, it, expect } from 'vitest'
import { arrangePaletteCategories, DEPRECATED, paletteCategoryLabel, paletteSectionOf, splitPaletteSections, WEB_AUTOMATION, AI_GROUP_PREFIX } from './paletteSections.js'

describe('palette sections', () => {
  it('reads a category section and label from its nodes', () => {
    const acme = [{ type: 'acme-crm.list_deals', section: 'web_automation', category_label: 'Acme CRM' }]
    expect(paletteSectionOf(acme)).toBe(WEB_AUTOMATION)
    expect(paletteCategoryLabel(acme)).toBe('Acme CRM')
    expect(paletteSectionOf([{ type: 'core.if', section: 'nodes' }])).toBe('nodes')
    expect(paletteSectionOf([{ type: 'core.if' }])).toBe('nodes') // an older CLI sends no section
    expect(paletteCategoryLabel([{ type: 'core.if' }])).toBe('')
  })

  it('puts web automations first, by name, and keeps both sections', () => {
    const cats = [
      { id: 'control', label: 'CONTROL', section: 'nodes' },
      { id: 'x', label: 'X', section: WEB_AUTOMATION },
      { id: 'acme-crm', label: 'ACME CRM', section: WEB_AUTOMATION },
      { id: 'data', label: 'DATA', section: 'nodes' },
    ]
    const [web, rest] = splitPaletteSections(cats)
    expect(web.categories.map(c => c.id)).toEqual(['acme-crm', 'x'])
    expect(rest.categories.map(c => c.id)).toEqual(['control', 'data'])
    expect(splitPaletteSections([]).map(s => s.categories.length)).toEqual([0, 0])
  })
})

describe('arrangePaletteCategories', () => {
  const cats = [
    { id: 'ai', label: 'AI', section: 'nodes', nodes: [
      { subtype: 'agent.ask', label: 'Agent Ask', category: 'ai' },
      { subtype: 'ai.chat', label: 'AI Chat (deprecated)', category: 'ai' },
    ] },
    { id: 'comm', label: 'COMM', section: 'nodes', nodes: [{ subtype: 'comm.bluesky', label: 'Bluesky', category: 'comm' }] },
    { id: 'service', label: 'SERVICE', section: 'nodes', nodes: [
      { subtype: 'service.bluesky', label: 'Bluesky', category: 'service' },
      { subtype: 'service.github', label: 'GitHub', category: 'service' },
      { subtype: 'service.openrouter', label: 'OpenRouter (deprecated)', category: 'service' },
    ] },
    { id: 'triggers', label: 'TRIGGERS', section: 'nodes', nodes: [{ subtype: 'trigger.manual', label: 'Manual Trigger', category: 'triggers' }] },
  ]

  it('puts triggers first and deprecated nodes in a collapsed group at the end', () => {
    const out = arrangePaletteCategories(cats)
    expect(out.map(c => c.id)).toEqual(['triggers', 'ai', 'comm', 'service', DEPRECATED])
    const dep = out[out.length - 1]
    expect(dep.collapsedByDefault).toBe(true)
    expect(dep.nodes.map(n => [n.subtype, n.category])).toEqual([['ai.chat', 'ai'], ['service.openrouter', 'service']])
    expect(out.find(c => c.id === 'ai').nodes.map(n => n.subtype)).toEqual(['agent.ask'])
  })

  it('keeps both same-named node types and marks the service one', () => {
    const out = arrangePaletteCategories(cats)
    expect(out.find(c => c.id === 'comm').nodes[0].label).toBe('Bluesky')
    expect(out.find(c => c.id === 'service').nodes.map(n => n.label)).toEqual(['Bluesky (API)', 'GitHub'])
  })

  it('adds no deprecated group when nothing is deprecated', () => {
    expect(arrangePaletteCategories([cats[1]]).map(c => c.id)).toEqual(['comm'])
  })
})

describe('AI sub-node palette groups', () => {
  const n = (type, label, schema = { fields: [] }) => ({ subtype: type, label, schema })
  const cats = [
    { id: 'triggers', label: 'TRIGGERS', section: 'nodes', nodes: [n('trigger.manual', 'Manual')] },
    { id: 'ai', label: 'AI', section: 'nodes', nodes: [
      n('ai.agent_demo', 'Agent', { fields: [], sub_inputs: [{ handle: 'ai_tool', label: 'Tool' }] }),
      n('ai.tool_calc', 'Calculator', { fields: [], sub_output: 'ai_tool' }),
      n('ai.model_demo', 'Demo Chat Model', { fields: [], sub_output: 'ai_languageModel' }),
      n('ai.memory_demo', 'Window Memory', { fields: [], sub_output: 'ai_memory' }),
    ] },
    { id: 'http', label: 'HTTP', section: 'nodes', nodes: [
      n('http.tool', 'HTTP Request Tool', { fields: [], sub_output: 'ai_tool' }),
      n('http.request', 'HTTP Request'),
    ] },
    { id: 'vec', label: 'VEC', section: 'nodes', nodes: [n('vec.embed', 'Embeddings', { fields: [], sub_output: 'ai_embedding' })] },
  ]

  it('groups sub-nodes by kind after AI, roots stay in AI', () => {
    const out = arrangePaletteCategories(cats)
    expect(out.map(c => c.label)).toEqual(['TRIGGERS', 'AI', 'AI · Models', 'AI · Memory', 'AI · Tools', 'AI · Embeddings', 'HTTP'])
    expect(out.find(c => c.id === 'ai').nodes.map(x => x.subtype)).toEqual(['ai.agent_demo'])
    const tools = out.find(c => c.id === AI_GROUP_PREFIX + 'ai_tool')
    expect(tools.nodes.map(x => x.subtype)).toEqual(['ai.tool_calc', 'http.tool'])
    // A node keeps its own category for its colour and the inspector.
    expect(tools.nodes[1].category).toBe('http')
    expect(out.find(c => c.id === 'vec')).toBeUndefined()
  })

  it('leaves a palette without sub-nodes as it was', () => {
    const plain = [{ id: 'ai', label: 'AI', section: 'nodes', nodes: [n('ai.chat', 'Chat')] }]
    expect(arrangePaletteCategories(plain).map(c => c.id)).toEqual(['ai'])
  })
})
