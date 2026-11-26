import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import i18nOr from '../lib/i18nOr.js' // for messages set in callbacks
import { ListResources, CreateResource, ConnectPlatformOAuth } from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

/**
 * ResourcePickerField — searchable dropdown + expand button for external resources.
 *
 * Props:
 *   field: schema field object (field.resource = { type, create_label, param_field })
 *   value: current selected resource ID
 *   onChange: (newId) => void
 *   credentialId: string
 *   platform: string (e.g. "google_sheets")
 *   nodeConfig: object (full node config, for param_field resolution)
 */
export default function ResourcePickerField({ field, value, onChange, credentialId, platform, nodeConfig }) {
  const { t } = useTranslation()
  const [items, setItems] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [needsReauth, setNeedsReauth] = useState(false)
  const [reconnecting, setReconnecting] = useState(false)
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState(false)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [selectedLabel, setSelectedLabel] = useState('')

  const resourceType = field.resource?.type || ''
  // Use credentialId if set, otherwise fall back to platform name for auto-lookup
  const effectiveCredId = credentialId || platform

  function handleListResult(result) {
    if (result.needs_reauth) {
      setNeedsReauth(true)
      setError(i18nOr('ui6ConnMisc.auth_expired'))
      setItems([])
    } else if (result.error) {
      setError(result.error)
    } else {
      setNeedsReauth(false)
      setError(null)
      setItems(result.items || [])
    }
  }

  // Load items when expanded
  useEffect(() => {
    if (!expanded) return
    if (!effectiveCredId || !platform || !resourceType) return
    setLoading(true)
    setError(null)
    setNeedsReauth(false)
    ListResources(platform, resourceType, effectiveCredId, query)
      .then(handleListResult)
      .catch(e => setError(String(e)))
      .finally(() => setLoading(false))
  }, [expanded, effectiveCredId, platform, resourceType])

  // Re-load when query changes (debounced)
  useEffect(() => {
    if (!expanded || !effectiveCredId || !platform || !resourceType) return
    const timer = setTimeout(() => {
      setLoading(true)
      setError(null)
      ListResources(platform, resourceType, effectiveCredId, query)
        .then(handleListResult)
        .catch(e => setError(String(e)))
        .finally(() => setLoading(false))
    }, 300)
    return () => clearTimeout(timer)
  }, [query])

  // Reconnect: trigger OAuth flow, then re-fetch on success
  function handleReconnect() {
    setReconnecting(true)
    setError(null)
    const cleanup = EventsOn('conn:done', (data) => {
      cleanup()
      setReconnecting(false)
      if (data?.success) {
        setNeedsReauth(false)
        // Re-fetch resources with the refreshed credential
        setLoading(true)
        ListResources(platform, resourceType, effectiveCredId, query)
          .then(handleListResult)
          .catch(e => setError(String(e)))
          .finally(() => setLoading(false))
      } else {
        setError(data?.error || i18nOr('ui6ConnMisc.reconnect_failed'))
      }
    })
    ConnectPlatformOAuth(platform)
  }

  // Resolve selected label from items
  useEffect(() => {
    if (value && items.length > 0) {
      const found = items.find(i => i.id === value)
      if (found) setSelectedLabel(found.name)
    }
  }, [value, items])

  function selectItem(item) {
    onChange(item.id)
    setSelectedLabel(item.name)
    setExpanded(false)
    setQuery('')
  }

  async function handleCreate() {
    if (!newName.trim()) return
    setLoading(true)
    try {
      const result = await CreateResource(platform, resourceType, effectiveCredId, newName.trim())
      if (result.error) { setError(result.error); return }
      if (result.item) {
        setItems(prev => [result.item, ...prev])
        selectItem(result.item)
        setCreating(false)
        setNewName('')
      }
    } catch(e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="resource-picker">
      <div className="resource-picker-compact">
        <input
          type="text"
          className="resource-picker-input"
          placeholder={t('ui6ConnMisc.search', { type: resourceType })}
          value={query}
          onChange={e => { setQuery(e.target.value); if (!expanded) setExpanded(true) }}
          onFocus={() => setExpanded(true)}
        />
        {selectedLabel && !expanded && (
          <span className="resource-picker-selected" title={value}>{selectedLabel}</span>
        )}
        {!selectedLabel && value && !expanded && (
          <span className="resource-picker-selected" title={value}>{value}</span>
        )}
        <button
          type="button"
          className="resource-picker-expand-btn"
          title={t('ui6ConnMisc.browse_all')}
          onClick={() => setExpanded(e => !e)}
        >⊞</button>
      </div>

      {expanded && (
        <div className="resource-browser">
          <div className="resource-browser-header">
            <span className="resource-browser-title">{t('ui6ConnMisc.select', { label: field.label })}</span>
            {field.resource?.create_label && (
              <button
                type="button"
                className="resource-create-btn"
                onClick={() => setCreating(c => !c)}
              >
                + {field.resource.create_label}
              </button>
            )}
            <button
              type="button"
              className="resource-browser-close"
              onClick={() => setExpanded(false)}
            >✕</button>
          </div>

          {creating && (
            <div className="resource-create-row">
              <input
                type="text"
                placeholder={t('ui6ConnMisc.name_placeholder')}
                value={newName}
                onChange={e => setNewName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleCreate() }}
                autoFocus
              />
              <button type="button" onClick={handleCreate} disabled={loading}>{t('ui6ConnMisc.create')}</button>
              <button type="button" onClick={() => { setCreating(false); setNewName('') }}>{t('ui6ConnMisc.cancel')}</button>
            </div>
          )}

          {error && (
            <div className="resource-error">
              {needsReauth ? t('ui6ConnMisc.auth_expired_reconnect') : error}
              {needsReauth && (
                <button
                  type="button"
                  onClick={handleReconnect}
                  disabled={reconnecting}
                  style={{
                    display: 'block',
                    marginTop: 8,
                    padding: '6px 14px',
                    background: '#00b4d8',
                    color: '#fff',
                    border: 'none',
                    borderRadius: 4,
                    cursor: reconnecting ? 'wait' : 'pointer',
                    fontFamily: 'var(--font-mono)',
                    fontSize: 11,
                    opacity: reconnecting ? 0.6 : 1,
                  }}
                >
                  {reconnecting ? t('ui6ConnMisc.reconnecting') : t('ui6ConnMisc.reconnect_account')}
                </button>
              )}
            </div>
          )}
          {loading && <div className="resource-loading">{t('ui6ConnMisc.loading')}</div>}

          {!loading && (
            <ul className="resource-list">
              {items.map(item => (
                <li
                  key={item.id}
                  className={`resource-list-item${item.id === value ? ' selected' : ''}`}
                  onClick={() => selectItem(item)}
                >
                  <span className="resource-item-name">{item.name}</span>
                  {item.metadata?.modified_time && (
                    <span className="resource-item-meta">{item.metadata.modified_time}</span>
                  )}
                </li>
              ))}
              {items.length === 0 && !error && (
                <li className="resource-empty">
                  {effectiveCredId ? t('ui6ConnMisc.no_results') : t('ui6ConnMisc.no_credential')}
                </li>
              )}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}
