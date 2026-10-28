import { createContext, useContext, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { usePersonItems } from './usePersonItems.js'

// PersonItemsContext hands the items that wait for the person to every surface that shows
// them (the agent's circle, its pane, the dock, the inbox) from one poll, so a chime sounds once.
// Without a provider (a page rendered alone) there are no items and nothing to answer.
const none = { items: [], loaded: false, refresh: () => {}, answer: async () => ({ error: 'no provider' }), openAgent: null }
const PersonItemsContext = createContext(none)

export const usePersonItemsContext = () => useContext(PersonItemsContext)

// usePersonItemsValue is the one poll behind the context: App calls it, reads the count for
// its badge and provides the value. openAgent(item) shows the item's agent pane.
export function usePersonItemsValue({ openAgent = null, enabled = true } = {}) {
  const { t } = useTranslation()
  const alerts = useMemo(() => ({
    title: t('personItem.notifyTitle'),
    body: count => t('personItem.notifyBody', { count }),
  }), [t])
  const { items, loaded, refresh, answer } = usePersonItems({ enabled, alerts })
  return useMemo(() => ({ items, loaded, refresh, answer, openAgent }), [items, loaded, refresh, answer, openAgent])
}

export function PersonItemsProvider({ value, children }) {
  return <PersonItemsContext.Provider value={value}>{children}</PersonItemsContext.Provider>
}
