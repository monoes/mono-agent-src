import { ChevronRight, SlidersHorizontal } from 'lucide-react'
import { SectionInspector, RoleSectionControls } from './SectionInspector.jsx'
import RoleInspector from './RoleInspector.jsx'
import { documentEdges } from './sectionsGraph.js'
import { sectionsOrgEnabled } from './sectionsRuntimes.js'
import { bySection } from './budgetModel.js'
import { panelFoldBtnStyle, panelStripStyle } from './designerHelpers.js'

// The folding right panel: section inspector or role inspector.
export default function DesignerInspectorPanel({
  inspectorOpen, setInspectorOpen, sec, isLive, budget, nodes, handlePatch, selectedNode, selectedId,
  handleSetReportsTo, handlePromoteToRoot, setIconPickerFor, setDeleteTarget, orgName, automationData,
  engineOffline, onOpenWorkflow, setGrantDialog, fullAccessByRole, orgMeta, rolesAccess, cliValidation,
}) {
  return (
        inspectorOpen ? (
          <div style={{ width: 300, flexShrink: 0, minHeight: 0, borderLeft: '1px solid var(--border)', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
            <button onClick={() => setInspectorOpen(false)} title="Collapse role editor" style={panelFoldBtnStyle}>
              Collapse <ChevronRight size={11} />
            </button>
            <div style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
              {sec.selected ? (
                <SectionInspector
                  section={sec.selected}
                  budget={isLive ? bySection(budget.report)[sec.selected.name] : null}
                  nodes={nodes}
                  sections={sec.list}
                  edges={documentEdges(sec.list)}
                  onUpdate={(patch) => sec.update(sec.selected.name, patch)}
                  onPatchRole={handlePatch}
                  onRemoveEdge={sec.removeEdge}
                  onDelete={(moveTo) => sec.remove(sec.selected.name, moveTo)}
                />
              ) : (<>
              {sec.on && (
                <RoleSectionControls
                  node={selectedNode}
                  sections={sec.list}
                  onAssign={(section) => sec.assign(selectedId, section)}
                  onMakeLead={(section) => sec.update(section, { lead: selectedId })}
                />
              )}
              <RoleInspector
                node={selectedNode}
                allNodes={nodes}
                onPatch={(patch) => selectedId && handlePatch(selectedId, patch)}
                onSetReportsTo={(parentId) => selectedId && handleSetReportsTo(selectedId, parentId)}
                onPromoteToRoot={() => selectedId && handlePromoteToRoot(selectedId)}
                onOpenIconPicker={() => selectedId && setIconPickerFor(selectedId)}
                onDelete={() => selectedNode && setDeleteTarget(selectedNode)}
                orgName={orgName}
                grants={automationData.grants}
                automations={automationData.automations}
                engineOffline={engineOffline}
                grantsVersion={automationData.version}
                onGrantsChanged={automationData.refresh}
                onOpenWorkflow={onOpenWorkflow}
                onEditGrant={(role, automation, grant) => setGrantDialog({ role, automation, grant })}
                fullAccess={selectedId ? fullAccessByRole[selectedId] || null : null}
                orgRuntime={orgMeta?.runtime || ''}
                sectionsOrg={sectionsOrgEnabled(orgMeta)}
                onAccessChanged={() => { rolesAccess.refresh(); cliValidation.refresh() }}
              />
              </>)}
            </div>
          </div>
        ) : (
          <button
            onClick={() => setInspectorOpen(true)}
            title="Role editor"
            style={panelStripStyle('left')}
          >
            <SlidersHorizontal size={13} />
          </button>
        )
  )
}
