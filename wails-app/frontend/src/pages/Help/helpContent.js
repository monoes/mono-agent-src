// Help content, no React. Every shown string is a ui6SetHelp locale key that
// lazy() turns into a getter, so it reads in the current language. HELP_KEYS
// is the flat key -> text map of the same content.
//
// Every command quoted here was checked against `monoagentcli ... --help`.
// Permission wording follows the permission map of the 2026-11-22 review (r28).

import i18nOr from '../../lib/i18nOr.js'

export const PAGE_IDS = [
  'dashboard', 'noderunner', 'orgs', 'people', 'publication', 'communications',
  'vault', 'secretsVault', 'applications', 'documents', 'info', 'tasks',
  'connections', 'ai', 'logs', 'settings',
]

const go = (target, params) => ({ kind: 'goto', target, ...(params ? { params } : {}) })
// lazy replaces the locale key in each named field with a getter of its text.
function lazy(obj, ...fields) {
  for (const field of fields) {
    const key = obj[field]
    Object.defineProperty(obj, field, { enumerable: true, get: () => i18nOr(key) })
  }
  return obj
}
const coder = (prompt) => lazy({ kind: 'coder', prompt }, 'prompt')
const step = (text, action) => lazy(action ? { text, action } : { text }, 'text')

export const TOPICS = [
  {
    id: 'firstSteps',
    emoji: '👋',
    title: 'ui6SetHelp.firstSteps_title',
    blurb: 'ui6SetHelp.firstSteps_blurb',
    quip: 'ui6SetHelp.firstSteps_quip',
    steps: [
      step('ui6SetHelp.firstSteps_s1', go('settings')),
      step('ui6SetHelp.firstSteps_s2', go('dashboard')),
      step('ui6SetHelp.firstSteps_s3', go('noderunner', { openTemplates: true })),
      step('ui6SetHelp.firstSteps_s4', go('dashboard', { showGetStarted: true })),
    ],
  },
  {
    id: 'profiles',
    emoji: '🗂️',
    title: 'ui6SetHelp.profiles_title',
    blurb: 'ui6SetHelp.profiles_blurb',
    quip: 'ui6SetHelp.profiles_quip',
    steps: [
      step('ui6SetHelp.profiles_s1', go('dashboard')),
      step('ui6SetHelp.profiles_s2', go('dashboard')),
      step('ui6SetHelp.profiles_s3', go('settings')),
    ],
  },
  {
    id: 'connections',
    emoji: '🔌',
    title: 'ui6SetHelp.connections_title',
    blurb: 'ui6SetHelp.connections_blurb',
    quip: 'ui6SetHelp.connections_quip',
    steps: [
      step('ui6SetHelp.connections_s1', go('connections')),
      step('ui6SetHelp.connections_s2', go('connections')),
      step('ui6SetHelp.connections_s3', go('connections', { tab: 'session' })),
    ],
  },
  {
    id: 'workflows',
    emoji: '🧩',
    title: 'ui6SetHelp.workflows_title',
    blurb: 'ui6SetHelp.workflows_blurb',
    quip: 'ui6SetHelp.workflows_quip',
    steps: [
      step('ui6SetHelp.workflows_s1', go('noderunner', { openTemplates: true })),
      step('ui6SetHelp.workflows_s2', go('noderunner')),
      step('ui6SetHelp.workflows_s3', go('noderunner')),
      step('ui6SetHelp.workflows_s4', coder('ui6SetHelp.workflows_s4_prompt')),
    ],
  },
  {
    id: 'tasks',
    emoji: '📋',
    title: 'ui6SetHelp.tasks_title',
    blurb: 'ui6SetHelp.tasks_blurb',
    quip: 'ui6SetHelp.tasks_quip',
    steps: [
      step('ui6SetHelp.tasks_s1', go('tasks')),
      step('ui6SetHelp.tasks_s2', go('tasks')),
      step('ui6SetHelp.tasks_s3', go('tasks')),
    ],
  },
  {
    id: 'hil',
    emoji: '✋',
    title: 'ui6SetHelp.hil_title',
    blurb: 'ui6SetHelp.hil_blurb',
    quip: 'ui6SetHelp.hil_quip',
    steps: [
      step('ui6SetHelp.hil_s1', go('communications')),
      step('ui6SetHelp.hil_s2', go('communications')),
      step('ui6SetHelp.hil_s3', go('noderunner')),
      step('ui6SetHelp.hil_s4', go('settings', { section: 'jev' })),
    ],
    danger: { level: 3, quip: 'ui6SetHelp.hil_danger' },
  },
  {
    id: 'cards',
    emoji: '🃏',
    title: 'ui6SetHelp.cards_title',
    blurb: 'ui6SetHelp.cards_blurb',
    quip: 'ui6SetHelp.cards_quip',
    steps: [
      step('ui6SetHelp.cards_s1', go('communications')),
      step('ui6SetHelp.cards_s2', go('communications')),
      step('ui6SetHelp.cards_s3', go('communications')),
      step('ui6SetHelp.cards_s4', go('tasks')),
      step('ui6SetHelp.cards_s5', go('communications')),
      step('ui6SetHelp.cards_s8', go('communications')),
      step('ui6SetHelp.cards_s6', go('communications')),
      step('ui6SetHelp.cards_s7', go('communications')),
    ],
  },
  {
    id: 'orgs',
    emoji: '🏢',
    title: 'ui6SetHelp.orgs_title',
    blurb: 'ui6SetHelp.orgs_blurb',
    quip: 'ui6SetHelp.orgs_quip',
    steps: [
      step('ui6SetHelp.orgs_s1', go('orgs')),
      step('ui6SetHelp.orgs_s2', go('orgs', { tab: 'overview' })),
      step('ui6SetHelp.orgs_s3', coder('ui6SetHelp.orgs_s3_prompt')),
    ],
    danger: { level: 4, quip: 'ui6SetHelp.orgs_danger' },
  },
  {
    id: 'coderModes',
    emoji: '🧑‍💻',
    title: 'ui6SetHelp.coderModes_title',
    blurb: 'ui6SetHelp.coderModes_blurb',
    quip: 'ui6SetHelp.coderModes_quip',
    steps: [
      step('ui6SetHelp.coderModes_s1', go('settings')),
      step('ui6SetHelp.coderModes_s2', go('tasks')),
      step('ui6SetHelp.coderModes_s3', go('settings')),
      step('ui6SetHelp.coderModes_s4', go('ai')),
      step('ui6SetHelp.coderModes_s5', coder('ui6SetHelp.coderModes_s5_prompt')),
    ],
    danger: { level: 5, quip: 'ui6SetHelp.coderModes_danger' },
  },
  {
    id: 'extension',
    emoji: '🧭',
    title: 'ui6SetHelp.extension_title',
    blurb: 'ui6SetHelp.extension_blurb',
    quip: 'ui6SetHelp.extension_quip',
    steps: [
      step('ui6SetHelp.extension_s1', go('settings')),
      step('ui6SetHelp.extension_s2', go('settings')),
      step('ui6SetHelp.extension_s3', go('settings', { section: 'health' })),
      step('ui6SetHelp.extension_s4', go('settings')),
    ],
    danger: { level: 4, quip: 'ui6SetHelp.extension_danger' },
  },
  {
    id: 'vault',
    emoji: '🔐',
    title: 'ui6SetHelp.vault_title',
    blurb: 'ui6SetHelp.vault_blurb',
    quip: 'ui6SetHelp.vault_quip',
    steps: [
      step('ui6SetHelp.vault_s1', go('secretsVault')),
      step('ui6SetHelp.vault_s2', go('secretsVault')),
      step('ui6SetHelp.vault_s3', go('secretsVault')),
    ],
    danger: { level: 4, quip: 'ui6SetHelp.vault_danger' },
  },
  {
    id: 'apiGateway',
    emoji: '🚪',
    title: 'ui6SetHelp.apiGateway_title',
    blurb: 'ui6SetHelp.apiGateway_blurb',
    quip: 'ui6SetHelp.apiGateway_quip',
    steps: [
      step('ui6SetHelp.apiGateway_s1', go('settings')),
      step('ui6SetHelp.apiGateway_s2', go('settings')),
      step('ui6SetHelp.apiGateway_s3', go('settings')),
    ],
  },
  {
    id: 'images',
    emoji: '🖼️',
    title: 'ui6SetHelp.images_title',
    blurb: 'ui6SetHelp.images_blurb',
    quip: 'ui6SetHelp.images_quip',
    steps: [
      step('ui6SetHelp.images_s1', go('vault')),
      step('ui6SetHelp.images_s2', go('noderunner', { openTemplates: true })),
      step('ui6SetHelp.images_s3', go('vault')),
    ],
  },
  {
    id: 'troubleshooting',
    emoji: '🩺',
    title: 'ui6SetHelp.troubleshooting_title',
    blurb: 'ui6SetHelp.troubleshooting_blurb',
    quip: 'ui6SetHelp.troubleshooting_quip',
    steps: [
      step('ui6SetHelp.troubleshooting_s1', go('settings', { section: 'health' })),
      step('ui6SetHelp.troubleshooting_s2', go('logs')),
      step('ui6SetHelp.troubleshooting_s3', go('dashboard')),
      step('ui6SetHelp.troubleshooting_s4', coder('ui6SetHelp.troubleshooting_s4_prompt')),
    ],
  },
  {
    id: 'updates',
    emoji: '⬆️',
    title: 'ui6SetHelp.updates_title',
    blurb: 'ui6SetHelp.updates_blurb',
    quip: 'ui6SetHelp.updates_quip',
    steps: [
      step('ui6SetHelp.updates_s1', go('settings', { section: 'version' })),
      step('ui6SetHelp.updates_s2', go('settings', { section: 'version' })),
    ],
  },
].map(topic => {
  if (topic.danger) lazy(topic.danger, 'quip')
  return lazy(topic, 'title', 'blurb', 'quip')
})

// The Permission Pit: each stage digs deeper. Consequences follow the
// permission map; none of the humor replaces the warning.
export const PIT_STAGES = [
  {
    level: 1,
    permission: 'ui6SetHelp.pit1_permission',
    label: 'ui6SetHelp.pit1_label',
    consequence: 'ui6SetHelp.pit1_consequence',
    mascotMood: 'happy',
  },
  {
    level: 2,
    permission: 'ui6SetHelp.pit2_permission',
    label: 'ui6SetHelp.pit2_label',
    consequence: 'ui6SetHelp.pit2_consequence',
    mascotMood: 'curious',
  },
  {
    level: 3,
    permission: 'ui6SetHelp.pit3_permission',
    label: 'ui6SetHelp.pit3_label',
    consequence: 'ui6SetHelp.pit3_consequence',
    mascotMood: 'nervous',
  },
  {
    level: 4,
    permission: 'ui6SetHelp.pit4_permission',
    label: 'ui6SetHelp.pit4_label',
    consequence: 'ui6SetHelp.pit4_consequence',
    mascotMood: 'sweating',
  },
  {
    level: 5,
    permission: 'ui6SetHelp.pit5_permission',
    label: 'ui6SetHelp.pit5_label',
    consequence: 'ui6SetHelp.pit5_consequence',
    mascotMood: 'panicking',
  },
].map(stage => lazy(stage, 'permission', 'label', 'consequence'))

export const EASTER_EGGS = [
  { trigger: 'konami', line: 'Up, up, down, down, left, right, left, right, B, A. You have unlocked: nothing, but you did it with style.' },
  { trigger: 'mascot-click-7', line: 'Seven pokes. The mascot would like you to know that it is a very good listener and a very poor punching bag.' },
  { trigger: 'type:sudo', line: 'Nice try. Permissions here are raised by you, in Settings or with --yes-i-understand, and not by magic words.' },
  { trigger: 'type:rm -rf', line: 'Typing it is free; running it is not. Agents with full access can really do this, which is why the pit exists.' },
  { trigger: 'type:please', line: 'Politeness noted. Agents still ask before sending anything as you, whatever your manners.' },
  { trigger: 'type:hello', line: 'Hello. The hello template says hi back, and it needs no credentials.' },
  { trigger: 'type:xyzzy', line: 'Nothing happens. (Some adventures never end.)' },
  { trigger: 'type:42', line: 'The answer, but you will want the question: which profile is this for?' },
  { trigger: 'type:vim', line: 'You can leave. The help page has no modes, only a polite exit button.' },
  { trigger: 'type:exit', line: 'There is no shell in here, but the sidebar is on the left.' },
  { trigger: 'type:matrix', line: 'There is no spoon, only workflows, and they are all visible in the editor.' },
  { trigger: 'type:coffee', line: 'Brewing. Agents work for tokens, not caffeine, but you go ahead.' },
  { trigger: 'type:bug', line: 'Found one? Run doctor first, then tell us. Both help.' },
  { trigger: 'idle-60s', line: 'Still there? Take your time. Nothing here runs while you read.' },
  { trigger: 'pit-bottom-reached', line: 'You reached the bottom. Every stage above it is cheaper, and almost always enough.' },
  { trigger: 'all-topics-read', line: 'You have read every topic. That makes you the most prepared person who has ever pressed Run.' },
]

const KEY_BASE = 'help'
export const HELP_KEYS = (() => {
  const out = {}
  for (const t of TOPICS) {
    const p = `${KEY_BASE}.topics.${t.id}`
    out[`${p}.title`] = t.title
    out[`${p}.blurb`] = t.blurb
    out[`${p}.quip`] = t.quip
    t.steps.forEach((s, i) => {
      out[`${p}.steps.${i}.text`] = s.text
      if (s.action?.kind === 'coder') out[`${p}.steps.${i}.prompt`] = s.action.prompt
    })
    if (t.danger) out[`${p}.danger.quip`] = t.danger.quip
  }
  for (const s of PIT_STAGES) {
    const p = `${KEY_BASE}.pit.${s.level}`
    out[`${p}.permission`] = s.permission
    out[`${p}.label`] = s.label
    out[`${p}.consequence`] = s.consequence
  }
  EASTER_EGGS.forEach((e, i) => { out[`${KEY_BASE}.eggs.${i}.line`] = e.line })
  return out
})()
