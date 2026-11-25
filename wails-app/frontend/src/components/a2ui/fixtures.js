// Dev fixtures: the plan page's three surfaces as ui.surface payloads, for
// eyeballing and tests. Each is a list of payloads to fold in seq order.
const v = m => ({ version: '1.0', ...m })
const t = (id, text, variant) => ({ id, component: 'Text', text, ...(variant ? { variant } : {}) })
const btn = (id, label, name, variant, context = {}) => [
  t(`${id}-l`, label),
  { id, component: 'Button', child: `${id}-l`, variant, action: { event: { name, context } } },
]

export const hilApproval = [
  { surfaceId: 'hil-482', provenance: 'system', ref: { kind: 'hil', id: '482' }, message: v({ createSurface: {
    surfaceId: 'hil-482', dataModel: { draft: 'Thanks Sara, happy to share the template.' },
    components: [
      { id: 'root', component: 'Card', child: 'col' },
      { id: 'col', component: 'Column', children: ['title', 'meta', 'draft', 'risk', 'divider', 'actions'] },
      t('title', 'Comment on LinkedIn post'),
      { id: 'meta', component: 'Row', children: ['acct', 'badge'] },
      t('acct', 'Account: @yourname', 'caption'),
      { id: 'badge', component: 'Badge', text: 'sends publicly', tone: 'warning' },
      { id: 'draft', component: 'TextField', label: 'Reply', variant: 'longText', value: { '@path': '/draft' } },
      { id: 'risk', component: 'CheckBox', label: 'I checked the text', value: { '@path': '/ok' } },
      { id: 'divider', component: 'Divider' },
      { id: 'actions', component: 'Row', children: ['approve', 'reject'] },
      ...btn('approve', 'Approve and send', 'hil.approve', 'primary', { id: '482', text: { '@path': '/draft' } }),
      ...btn('reject', 'Reject', 'hil.reject', 'danger', { id: '482' }),
    ] } }) },
]

export const structuredQuestion = [
  { surfaceId: 'ask-17', provenance: 'agent', message: v({ createSurface: {
    surfaceId: 'ask-17', dataModel: { audience: ['founders'], note: '' },
    components: [
      { id: 'root', component: 'Column', children: ['q', 'pick', 'note', 'go'] },
      t('q', 'Which audience should the outreach target?'),
      { id: 'pick', component: 'ChoicePicker', variant: 'mutuallyExclusive', value: { '@path': '/audience' }, options: [
        { label: 'Founders (214 people)', value: 'founders' }, { label: 'Engineers (530)', value: 'engineers' }, { label: 'Both, split A/B', value: 'both' }] },
      { id: 'note', component: 'TextField', label: 'Optional note', placeholder: 'Optional note...', value: { '@path': '/note' } },
      ...btn('go', 'Continue', 'answer', 'primary', { audience: { '@path': '/audience' }, note: { '@path': '/note' } }),
    ] } }) },
]

export const missionPlan = [
  { surfaceId: 'mission-plan', provenance: 'system', ref: { kind: 'mission', id: '17' }, message: v({ createSurface: {
    surfaceId: 'mission-plan', dataModel: { spent: 1.8, budget: 5 },
    components: [
      { id: 'root', component: 'Card', child: 'col' },
      { id: 'col', component: 'Column', children: ['title', 'budget', 'tasks', 'actions'] },
      t('title', 'Plan: grow newsletter list (3 tasks)'),
      { id: 'budget', component: 'Progress', label: 'Budget used (USD)', value: { '@path': '/spent' }, max: { '@path': '/budget' } },
      { id: 'tasks', component: 'Column', children: ['t1', 't2', 't3'] },
      { id: 't1', component: 'Row', children: ['t1n', 't1b'] }, t('t1n', 'Collect leads'), { id: 't1b', component: 'Badge', text: 'done', tone: 'success' },
      { id: 't2', component: 'Row', children: ['t2n', 't2b'] }, t('t2n', 'Write sequence'), { id: 't2b', component: 'Badge', text: 'running', tone: 'info' },
      { id: 't3', component: 'Row', children: ['t3n', 't3b'] }, t('t3n', 'Review drafts'), { id: 't3b', component: 'Badge', text: 'needs you', tone: 'danger' },
      { id: 'actions', component: 'Row', children: ['ok', 'stop'] },
      ...btn('ok', 'Approve plan', 'mission.approve', 'primary', { id: '17' }),
      ...btn('stop', 'Stop', 'mission.stop', 'danger', { id: '17' }),
    ] } }) },
]
