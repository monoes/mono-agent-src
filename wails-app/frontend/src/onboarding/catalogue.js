// "What you can do with MonoAgent": the eleven cards of the last step
// (docs/mastermind/specs/onboarding-mockups/CONTENT.md, step 10). Each opens a
// real page of the app (a key of App.jsx's page map, PAGE_IDS), with opts for
// the page's navData. A card with no page of its own opens where it lives:
// chat starts from the AI agents page, schedules and triggers are trigger
// nodes in Workflows, social automations and the Library are the editor's
// Templates dialog (which opens the monoes.me Library), API and MCP are in
// Settings.
export const CATALOGUE = [
  { id: 'workflows', icon: 'workflow', page: 'noderunner',
    titleKey: 'onboarding.finish.cat.workflows.title', lineKey: 'onboarding.finish.cat.workflows.line', whereKey: 'onboarding.finish.cat.workflows.where' },
  { id: 'orgs', icon: 'org', page: 'orgs',
    titleKey: 'onboarding.finish.cat.orgs.title', lineKey: 'onboarding.finish.cat.orgs.line', whereKey: 'onboarding.finish.cat.orgs.where' },
  { id: 'chat', icon: 'chat', page: 'ai',
    titleKey: 'onboarding.finish.cat.chat.title', lineKey: 'onboarding.finish.cat.chat.line', whereKey: 'onboarding.finish.cat.chat.where' },
  { id: 'documents', icon: 'doc', page: 'documents',
    titleKey: 'onboarding.finish.cat.documents.title', lineKey: 'onboarding.finish.cat.documents.line', whereKey: 'onboarding.finish.cat.documents.where' },
  { id: 'people', icon: 'people', page: 'people',
    titleKey: 'onboarding.finish.cat.people.title', lineKey: 'onboarding.finish.cat.people.line', whereKey: 'onboarding.finish.cat.people.where' },
  { id: 'social', icon: 'share', page: 'noderunner', opts: { openTemplates: true },
    titleKey: 'onboarding.finish.cat.social.title', lineKey: 'onboarding.finish.cat.social.line', whereKey: 'onboarding.finish.cat.social.where' },
  { id: 'schedules', icon: 'clock', page: 'noderunner',
    titleKey: 'onboarding.finish.cat.schedules.title', lineKey: 'onboarding.finish.cat.schedules.line', whereKey: 'onboarding.finish.cat.schedules.where' },
  { id: 'tasks', icon: 'board', page: 'tasks',
    titleKey: 'onboarding.finish.cat.tasks.title', lineKey: 'onboarding.finish.cat.tasks.line', whereKey: 'onboarding.finish.cat.tasks.where' },
  { id: 'secrets', icon: 'key', page: 'secretsVault',
    titleKey: 'onboarding.finish.cat.secrets.title', lineKey: 'onboarding.finish.cat.secrets.line', whereKey: 'onboarding.finish.cat.secrets.where' },
  { id: 'api', icon: 'plug', page: 'settings',
    titleKey: 'onboarding.finish.cat.api.title', lineKey: 'onboarding.finish.cat.api.line', whereKey: 'onboarding.finish.cat.api.where' },
  { id: 'library', icon: 'library', page: 'noderunner', opts: { openTemplates: true },
    titleKey: 'onboarding.finish.cat.library.title', lineKey: 'onboarding.finish.cat.library.line', whereKey: 'onboarding.finish.cat.library.where' },
]
