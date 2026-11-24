// Page ids App.jsx can route to (its persistentPages keys, minus the
// conditional controlroom; detail pages need an id so are not link targets).
// pages/Help/helpActions.test.js fails if App.jsx drifts from this list.
export const PAGE_IDS = Object.freeze([
  'dashboard', 'noderunner', 'people', 'communications', 'publication',
  'connections', 'vault', 'secretsVault', 'applications', 'documents',
  'info', 'tasks', 'ai', 'orgs', 'logs', 'settings', 'help',
])

// Ids navigate() accepts beyond the link targets: the conditional Control
// Room and the two detail views.
const EXTRA_NAVIGABLE = ['controlroom', 'profile', 'postDetail']

export function isNavigablePage(page) {
  return PAGE_IDS.includes(page) || EXTRA_NAVIGABLE.includes(page)
}
