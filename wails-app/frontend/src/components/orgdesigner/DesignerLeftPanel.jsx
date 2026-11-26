import { ChevronLeft, Search } from 'lucide-react'
import AutomationsDrawer from './AutomationsDrawer.jsx'
import RolePalette from './RolePalette.jsx'
import { panelFoldBtnStyle, panelStripStyle } from './designerHelpers.js'
import { useTranslation } from 'react-i18next'

// The folding left panel: Roles palette / Automations drawer.
export default function DesignerLeftPanel({
  paletteOpen, setPaletteOpen, leftTab, setLeftTab, orgName, automationData,
  handleAutomationDragStart, onOpenWorkflow, onCreateWorkflow, handlePaletteDragStart, quickAddRole,
}) {
  const { t } = useTranslation()
  return (
        paletteOpen ? (
          <div style={{ width: 240, flexShrink: 0, minHeight: 0, borderRight: '1px solid var(--border)', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
            <div style={{ display: 'flex', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
              <div role="tablist" aria-label={t('ui6OrgsCanvas.left_panel')} style={{ display: 'flex', flex: 1 }}>
                {[['roles', t('ui6OrgsCanvas.tab_roles')], ['automations', t('ui6OrgsCanvas.tab_automations')]].map(([id, label]) => (
                  <button
                    key={id}
                    role="tab"
                    aria-selected={leftTab === id}
                    onClick={() => setLeftTab(id)}
                    style={{ ...panelFoldBtnStyle, flex: 1, borderBottom: leftTab === id ? '2px solid var(--cyan, #00b4d8)' : '2px solid transparent', color: leftTab === id ? 'var(--text)' : 'var(--text-muted)' }}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <button onClick={() => setPaletteOpen(false)} title={t('ui6OrgsCanvas.collapse_panel')} aria-label={t('ui6OrgsCanvas.collapse_panel')} style={{ ...panelFoldBtnStyle, borderBottom: 'none' }}>
                <ChevronLeft size={11} />
              </button>
            </div>
            <div style={{ flex: 1, minHeight: 0, overflow: 'hidden', display: 'flex' }}>
              {leftTab === 'automations' ? (
                <AutomationsDrawer
                  orgName={orgName}
                  automations={automationData.automations}
                  grants={automationData.grants}
                  loading={automationData.loading}
                  error={automationData.error}
                  onRefresh={automationData.refresh}
                  onDragStart={handleAutomationDragStart}
                  onOpenWorkflow={onOpenWorkflow}
                  onCreateWorkflow={onCreateWorkflow}
                />
              ) : (
                <RolePalette onDragStart={handlePaletteDragStart} onQuickAdd={quickAddRole} />
              )}
            </div>
          </div>
        ) : (
          <button
            onClick={() => setPaletteOpen(true)}
            title={t('ui6OrgsCanvas.search_add')}
            style={panelStripStyle('right')}
          >
            <Search size={13} />
          </button>
        )
  )
}
