import { Workflow } from 'lucide-react'
import NamePrompt from './NamePrompt.jsx'
import IconPickerModal from './IconPickerModal.jsx'
import DeleteRoleModal from './DeleteRoleModal.jsx'
import GrantDialog from './GrantDialog.jsx'
import { iconUrl } from './roleIcons.js'
import { useTranslation } from 'react-i18next'

// Prompts, modals, the grant dialog and the drag ghost layered over the designer.
export default function DesignerOverlays({
  sec, iconPickerFor, iconPickerNode, handleIconSelected, setIconPickerFor, deleteTarget, setDeleteTarget,
  nodes, handleDeleteConfirm, grantDialog, setGrantDialog, orgName, automationData, handleGrantSaved,
  handleDenyBash, ghost,
}) {
  const { t } = useTranslation()
  return (
    <>
      {sec.prompt && (
        <NamePrompt
          key={sec.prompt.kind + (sec.prompt.from || '')}
          title={sec.prompt.kind === 'section' ? t('ui6OrgsCanvas.new_section') : t('ui6OrgsCanvas.doc_from_to', { from: sec.prompt.from, to: sec.prompt.to })}
          hint={sec.prompt.kind === 'section'
            ? (sec.on ? t('ui6OrgsCanvas.hint_move_role') : t('ui6OrgsCanvas.hint_first_section'))
            : t('ui6OrgsCanvas.hint_doc_type')}
          initial={sec.prompt.kind === 'section' ? sec.suggestName() : ''}
          submitLabel={sec.prompt.kind === 'section' ? t('ui6OrgsCanvas.create_section') : t('ui6OrgsCanvas.add_edge')}
          error={sec.prompt.error || ''}
          onSubmit={sec.submitPrompt}
          onCancel={() => sec.setPrompt(null)}
        />
      )}
      <IconPickerModal
        open={!!iconPickerFor}
        currentIconId={iconPickerNode?.icon}
        onSelect={handleIconSelected}
        onClose={() => setIconPickerFor(null)}
      />
      <DeleteRoleModal
        open={!!deleteTarget}
        node={deleteTarget}
        allNodes={nodes}
        onConfirm={handleDeleteConfirm}
        onClose={() => setDeleteTarget(null)}
      />
      <GrantDialog
        open={!!grantDialog}
        orgName={orgName}
        role={grantDialog?.role}
        automation={grantDialog?.automation}
        grant={grantDialog?.grant}
        autonomy={automationData.autonomy}
        onClose={() => setGrantDialog(null)}
        onSaved={handleGrantSaved}
        onDenyBash={handleDenyBash}
      />

      {ghost && (
        <div
          style={{
            position: 'fixed', left: ghost.x + 12, top: ghost.y - 16, zIndex: 1000,
            pointerEvents: 'none', display: 'flex', alignItems: 'center', gap: 6,
            padding: '4px 8px 4px 4px', borderRadius: 16,
            background: 'rgba(8,12,20,0.92)', border: `1.5px solid ${ghost.kind === 'automation' ? 'rgba(167,139,250,0.6)' : 'rgba(0,180,216,0.4)'}`,
            boxShadow: '0 4px 16px rgba(0,0,0,0.4)',
          }}
        >
          {ghost.kind === 'automation'
            ? <Workflow size={18} color="#a78bfa" style={{ margin: 2 }} />
            : <img src={iconUrl(ghost.item.id)} alt="" style={{ width: 22, height: 22, borderRadius: '50%' }} />}
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#e2e8f0', whiteSpace: 'nowrap' }}>
            {ghost.kind === 'automation' ? (ghost.item.workflow_name || ghost.item.alias) : ghost.item.label}
          </span>
        </div>
      )}
    </>
  )
}
